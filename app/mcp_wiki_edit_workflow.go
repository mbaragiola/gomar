package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const wikiEditWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/wiki-edit-proposals"
const wikiEditWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/wiki-edit-proposals"
const wikiEditWireToken = "/api/v1/auth/mcp-wiki-edit-proposal-token"

type wikiEditWireReceipt struct {
	ID             int64  `json:"id"`
	Status         string `json:"status"`
	Expires        string `json:"expires_at"`
	ArticleID      *int64 `json:"article_id,omitempty"`
	ArticleVersion *int64 `json:"article_version,omitempty"`
}
type wikiEditWireDetails struct {
	wikiEditWireReceipt
	Edit   *wikiEditPreview `json:"edit,omitempty"`
	Digest string           `json:"digest,omitempty"`
}
type wikiEditWireRow struct {
	wikiEditWireReceipt
	tokenID, expiry                    int64
	inputDigest, reviewDigest, preview []byte
}

const wikiEditWireColumns = `id,token_id,status,expires_at,article_id,article_version,input_digest,review_digest,edit_preview`

func scanWikiEditProposal(row interface{ Scan(...any) error }) (wikiEditWireRow, error) {
	var p wikiEditWireRow
	err := row.Scan(&p.ID, &p.tokenID, &p.Status, &p.expiry, &p.ArticleID, &p.ArticleVersion, &p.inputDigest, &p.reviewDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPWikiEditProposals(add func(string, string, http.HandlerFunc)) {
	if !a.enableMCPWikiEditProposals {
		return
	}
	add(wikiEditWireToken, http.MethodPost, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
		return a.issueProposalToken(w, r, u, wikiEditKeyPurpose)
	}))
	add(wikiEditWireToken+"/{token}", http.MethodDelete, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
		return a.revokeProposalToken(w, r, u, wikiEditKeyPurpose)
	}))
	add(wikiEditWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listWikiEditProposals))
	add(wikiEditWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewWikiEditProposal))
	add(wikiEditWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideWikiEditProposal))
	add(wikiEditWireAPI, http.MethodPost, a.submitWikiEditProposal)
}

func wikiEditScopeFrom(s wikiProposalScope) wikiEditScope {
	return wikiEditScope{Actor: s.actor, Entity: s.entity, Outlet: s.outlet}
}

func wikiEditWireInputDigest(s wikiEditScope, input wikiEditInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope wikiEditScope
		Input wikiEditInput
	}{wikiEditProposalPurpose, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitWikiEditProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptWikiEditProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptWikiEditProposal(w http.ResponseWriter, r *http.Request) error {
	if r.URL.RawQuery != "" || len(r.Header.Values("Cookie")) != 0 {
		return badInput
	}
	hash, err := parseReadBearer(r)
	if err != nil {
		return err
	}
	scope, err := wikiProposalPath(r)
	if err != nil {
		return err
	}
	raw, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	var body struct {
		RequestID string          `json:"request_id"`
		Edit      json.RawMessage `json:"edit"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "edit"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in, err := decodeWikiEditProposal(body.Edit)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	token, expiry, err := a.loadProposalKey(ctx, tx, hash, &scope, wikiEditKeyPurpose)
	if err != nil {
		return err
	}
	if err = lockPostgresEntity(ctx, tx, scope.entity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return missingEntity
		}
		return err
	}
	if err = requireProposalScope(ctx, tx, scope, wikiEditKeyPurpose); err != nil {
		return err
	}
	s := wikiEditScopeFrom(scope)
	inputDigest := wikiEditWireInputDigest(s, in)
	p, err := scanWikiEditProposal(tx.QueryRowContext(ctx, `SELECT `+wikiEditWireColumns+` FROM mcp_wiki_edit_proposals
		WHERE actor_id=$1 AND entity_id=$2 AND outlet_id=$3 AND request_id=$4 FOR UPDATE`,
		scope.actor, scope.entity, scope.outlet, body.RequestID))
	now := a.tokenTime()
	if now.Unix() >= expiry {
		return unauthenticated
	}
	status := http.StatusOK
	switch err {
	case nil:
		if subtle.ConstantTimeCompare(p.inputDigest, inputDigest[:]) != 1 {
			return wikiProposalConflict
		}
		if p.Status == "pending" && p.expiry <= now.Unix() {
			p.Status = "expired"
		}
	case wikiProposalMissing:
		var pending, recent int64
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE expires_at>$2 AND status='pending'),COUNT(*)
			FROM mcp_wiki_edit_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := prepareWikiEditProposal(ctx, tx, s, in)
		if err != nil {
			return err
		}
		reviewDigest := prepared.digest()
		preview, err := json.Marshal(prepared.Preview)
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		p, err = scanWikiEditProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_wiki_edit_proposals
			(token_id,actor_id,entity_id,outlet_id,request_id,input_digest,review_digest,
			edit_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+wikiEditWireColumns,
			token, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], reviewDigest[:], preview, now.Unix(), expiry))
		if err != nil {
			return err
		}
		status = http.StatusCreated
	default:
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	p.ArticleID, p.ArticleVersion = nil, nil
	jsonResponse(w, status, p.wikiEditWireReceipt)
	return nil
}

func (a *app) listWikiEditProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, password, err := a.beginTypedProposalReview(r, u, wikiEditKeyPurpose, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND (t.revoked_at IS NOT NULL OR t.password_digest<>$5) THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.article_id,p.article_version
		FROM mcp_wiki_edit_proposals p JOIN mcp_wiki_proposal_tokens t ON t.id=p.token_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL AND t.password_digest=$5) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), password, now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []wikiEditWireReceipt{}
	for rows.Next() {
		var item wikiEditWireReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.ArticleID, &item.ArticleVersion); err != nil {
			return err
		}
		item.Expires = time.Unix(expiry, 0).UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, map[string]any{"proposals": items})
	return nil
}

func lookupWikiEditProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope) (wikiEditWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return wikiEditWireRow{}, wikiProposalMissing
	}
	return scanWikiEditProposal(tx.QueryRowContext(r.Context(), `SELECT `+wikiEditWireColumns+` FROM mcp_wiki_edit_proposals
		WHERE id=$1 AND actor_id=$2 AND entity_id=$3 AND outlet_id=$4 FOR UPDATE`, id, s.actor, s.entity, s.outlet))
}

func (p wikiEditWireRow) validatedEdit(s wikiProposalScope) (wikiEditProposal, error) {
	var draft wikiEditProposal
	draft.Scope = wikiEditScopeFrom(s)
	if len(p.reviewDigest) != 32 || json.Unmarshal(p.preview, &draft.Preview) != nil {
		return draft, staleKnowledge
	}
	actual := draft.digest()
	if subtle.ConstantTimeCompare(actual[:], p.reviewDigest) != 1 {
		return draft, staleKnowledge
	}
	return draft, nil
}

func (a *app) viewWikiEditProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, password, err := a.beginTypedProposalReview(r, u, wikiEditKeyPurpose, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupWikiEditProposal(r, tx, s)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.typedProposalState(r.Context(), tx, p.Status, p.expiry, p.tokenID, password)
	if err != nil {
		return err
	}
	result := wikiEditWireDetails{wikiEditWireReceipt: p.wikiEditWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedEdit(s)
		if err != nil {
			return err
		}
		result.Edit = &draft.Preview
		result.Digest = hex.EncodeToString(p.reviewDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setWikiEditProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, articleID, articleVersion *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_wiki_edit_proposals SET status=$1,edit_preview=NULL,
		decided_at=$2,article_id=$3,article_version=$4 WHERE id=$5 AND status='pending'`, status, now, articleID, articleVersion, id))
}

func (a *app) decideWikiEditProposal(w http.ResponseWriter, r *http.Request, u user) error {
	raw, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	var input struct {
		Decision string `json:"decision"`
		Digest   string `json:"digest"`
		Confirm  bool   `json:"confirm"`
	}
	if err = decodeWikiProposalJSON(raw, &input, "decision", "digest", "confirm"); err != nil {
		return err
	}
	if !input.Confirm || (input.Decision != "approve" && input.Decision != "reject") || len(input.Digest) != 64 ||
		input.Digest != strings.ToLower(input.Digest) {
		return badInput
	}
	var confirmed [32]byte
	if _, err = hex.Decode(confirmed[:], []byte(input.Digest)); err != nil {
		return badInput
	}
	tx, s, password, err := a.beginTypedProposalReview(r, u, wikiEditKeyPurpose, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupWikiEditProposal(r, tx, s)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	switch p.Status {
	case "approved", "rejected":
		return wikiProposalConflict
	case "expired", "revoked":
		return wikiProposalUnavailable
	}
	state, err := a.typedProposalState(ctx, tx, p.Status, p.expiry, p.tokenID, password)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setWikiEditProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil, nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.reviewDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedEdit(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		article, err := a.applyWikiEditProposal(r, tx, u, wikiEditScopeFrom(s), draft, confirmed)
		if err != nil {
			return err
		}
		p.ArticleID, p.ArticleVersion = &article.ID, &article.Version
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setWikiEditProposalTerminal(ctx, tx, p.ID, p.Status, now, p.ArticleID, p.ArticleVersion); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.wikiEditWireReceipt)
	return nil
}
