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

const posRenameWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/pos-rename-proposals"
const posRenameWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/pos-rename-proposals"

type posRenameWireReceipt struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"`
	Expires         string `json:"expires_at"`
	ProductID       *int64 `json:"product_id,omitempty"`
	ProductRevision *int64 `json:"product_revision,omitempty"`
}
type posRenameWireDetails struct {
	posRenameWireReceipt
	Rename *posRenamePreview `json:"rename,omitempty"`
	Digest string            `json:"digest,omitempty"`
}
type posRenameWireRow struct {
	posRenameWireReceipt
	sessionID, expiry                  int64
	inputDigest, reviewDigest, preview []byte
}

const posRenameWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.product_id,p.product_revision,p.input_digest,p.review_digest,p.rename_preview`

func scanPOSRenameProposal(row interface{ Scan(...any) error }) (posRenameWireRow, error) {
	var p posRenameWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.ProductID, &p.ProductRevision, &p.inputDigest, &p.reviewDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPPOSRenameProposals(add func(string, string, http.HandlerFunc)) {
	if !a.posRenameProposalsEnabled() {
		return
	}
	add(posRenameWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listPOSRenameProposals))
	add(posRenameWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewPOSRenameProposal))
	add(posRenameWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decidePOSRenameProposal))
	add(posRenameWireAPI, http.MethodPost, a.submitPOSRenameProposal)
}

func posRenameScopeFrom(s wikiProposalScope) posRenameScope {
	return posRenameScope{Actor: s.actor, Entity: s.entity, Outlet: s.outlet}
}

func posRenameWireInputDigest(s posRenameScope, input posRenameInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope posRenameScope
		Input posRenameInput
	}{posRenameProposalPurpose, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitPOSRenameProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptPOSRenameProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptPOSRenameProposal(w http.ResponseWriter, r *http.Request) error {
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
	if len(raw) > 3072 {
		return badInput
	}
	var body struct {
		RequestID string          `json:"request_id"`
		Rename    json.RawMessage `json:"rename"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "rename"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in, err := decodePOSRename(body.Rename)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadPOSRenameSession(ctx, tx, hash, &scope)
	if err != nil {
		return err
	}
	s := posRenameScopeFrom(scope)
	if _, err = a.lockPOSRenameScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := posRenameWireInputDigest(s, in)
	p, err := scanPOSRenameProposal(tx.QueryRowContext(ctx, `SELECT `+posRenameWireColumns+` FROM mcp_pos_rename_proposals p
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND p.request_id=$4 FOR UPDATE OF p`,
		scope.actor, scope.entity, scope.outlet, body.RequestID))
	now := a.tokenTime()
	if now.Unix() >= expiry {
		return unauthenticated
	}
	status := http.StatusOK
	switch err {
	case nil:
		// A matching request belongs to its original browser login. A later
		// login cannot adopt that proposal by reusing the request ID.
		var sameLogin bool
		err = tx.QueryRowContext(ctx, `SELECT first.login_hash=new.login_hash FROM mcp_proposal_sessions first JOIN mcp_proposal_sessions new ON new.id=$2
			WHERE first.id=$1`, p.sessionID, session).Scan(&sameLogin)
		if err != nil {
			return err
		}
		if !sameLogin {
			return wikiProposalConflict
		}
		if subtle.ConstantTimeCompare(p.inputDigest, inputDigest[:]) != 1 {
			return wikiProposalConflict
		}
		if p.Status == "pending" && p.expiry <= now.Unix() {
			p.Status = "expired"
		}
	case wikiProposalMissing:
		var pending, recent int64
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE expires_at>$2 AND status='pending'),COUNT(*)
			FROM mcp_pos_rename_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := a.preparePOSRename(ctx, tx, s, in)
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
		p, err = scanPOSRenameProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_pos_rename_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,request_id,input_digest,review_digest,
			rename_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+posRenameWireColumns,
			session, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], reviewDigest[:], preview, now.Unix(), expiry))
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
	p.ProductID, p.ProductRevision = nil, nil
	jsonResponse(w, status, p.posRenameWireReceipt)
	return nil
}

func (a *app) listPOSRenameProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginPOSRenameReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.product_id,p.product_revision
		FROM mcp_pos_rename_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []posRenameWireReceipt{}
	for rows.Next() {
		var item posRenameWireReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.ProductID, &item.ProductRevision); err != nil {
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

func lookupPOSRenameProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte) (posRenameWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return posRenameWireRow{}, wikiProposalMissing
	}
	return scanPOSRenameProposal(tx.QueryRowContext(r.Context(), `SELECT `+posRenameWireColumns+` FROM mcp_pos_rename_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:]))
}

func (p posRenameWireRow) validatedPOSRename(s wikiProposalScope) (posRenameProposal, error) {
	var draft posRenameProposal
	draft.Scope = posRenameScopeFrom(s)
	if len(p.reviewDigest) != 32 || json.Unmarshal(p.preview, &draft.Preview) != nil {
		return draft, posChangedCatalog
	}
	actual := draft.digest()
	if subtle.ConstantTimeCompare(actual[:], p.reviewDigest) != 1 {
		return draft, posChangedCatalog
	}
	return draft, nil
}

func (a *app) viewPOSRenameProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginPOSRenameReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupPOSRenameProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.posRenameProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := posRenameWireDetails{posRenameWireReceipt: p.posRenameWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedPOSRename(s)
		if err != nil {
			return err
		}
		result.Rename = &draft.Preview
		result.Digest = hex.EncodeToString(p.reviewDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setPOSRenameProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, productID, revision *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_pos_rename_proposals SET status=$1,rename_preview=NULL,
		decided_at=$2,product_id=$3,product_revision=$4 WHERE id=$5 AND status='pending'`, status, now, productID, revision, id))
}

func (a *app) decidePOSRenameProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	tx, s, login, err := a.beginPOSRenameReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupPOSRenameProposal(r, tx, s, login)
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
	state, err := a.posRenameProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setPOSRenameProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil, nil); err != nil {
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
	draft, err := p.validatedPOSRename(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		product, err := a.applyPOSRename(r, tx, u, posRenameScopeFrom(s), draft, confirmed)
		if err != nil {
			return err
		}
		p.ProductID, p.ProductRevision = &product.ID, &product.Revision
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setPOSRenameProposalTerminal(ctx, tx, p.ID, p.Status, now, p.ProductID, p.ProductRevision); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.posRenameWireReceipt)
	return nil
}

func (a *app) beginPOSRenameReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
	var zero [32]byte
	s, err := wikiProposalPath(r)
	if err != nil {
		return nil, s, zero, err
	}
	s.actor = u.id
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil, s, zero, err
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err == nil {
		_, err = a.lockPOSRenameScope(r.Context(), tx, posRenameScopeFrom(s))
	}
	if err != nil {
		tx.Rollback()
		return nil, s, zero, err
	}
	login, err := proposalLoginHash(r)
	if err != nil {
		tx.Rollback()
		return nil, s, zero, err
	}
	return tx, s, login, nil
}

func (a *app) posRenameProposalState(ctx context.Context, tx *sql.Tx, p posRenameWireRow) (string, error) {
	if p.Status != "pending" {
		return p.Status, nil
	}
	if p.expiry <= a.tokenTime().Unix() {
		return "expired", nil
	}
	var revoked sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM mcp_proposal_sessions WHERE id=$1`, p.sessionID).Scan(&revoked); err != nil {
		return "", err
	}
	if revoked.Valid {
		return "revoked", nil
	}
	return "pending", nil
}

func (a *app) prunePOSRenameProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_pos_rename_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_pos_rename_proposals p SET status=ended.state,rename_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `WITH old AS (
		SELECT id FROM mcp_proposal_sessions WHERE expires_at<=$1 ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED
	) DELETE FROM mcp_proposal_sessions p USING old WHERE p.id=old.id`, now.Add(-wikiProposalRetention).Unix())
	return err
}
