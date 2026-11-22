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

const documentReviewWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/document-review-proposals"
const documentReviewWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/document-review-proposals"

type documentReviewWireReceipt struct {
	ID               int64  `json:"id"`
	Status           string `json:"status"`
	Expires          string `json:"expires_at"`
	DocumentID       *int64 `json:"document_id,omitempty"`
	DocumentRevision *int64 `json:"document_revision,omitempty"`
}
type documentReviewPreview struct {
	documentReviewInput
	Type       string `json:"type"`
	Currency   string `json:"currency"`
	TotalMinor int64  `json:"total_minor"`
}
type documentReviewWireDetails struct {
	documentReviewWireReceipt
	Review *documentReviewPreview `json:"review,omitempty"`
	Digest string                 `json:"digest,omitempty"`
}
type documentReviewWireRow struct {
	documentReviewWireReceipt
	sessionID, expiry                  int64
	inputDigest, reviewDigest, preview []byte
}

const documentReviewWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.document_id,p.document_revision,p.input_digest,p.review_digest,p.review_preview`

func scanDocumentReviewProposal(row interface{ Scan(...any) error }) (documentReviewWireRow, error) {
	var p documentReviewWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.DocumentID, &p.DocumentRevision, &p.inputDigest, &p.reviewDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPDocumentReviewProposals(add func(string, string, http.HandlerFunc)) {
	if !a.documentReviewProposalsEnabled() {
		return
	}
	add(documentReviewWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listDocumentReviewProposals))
	add(documentReviewWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewDocumentReviewProposal))
	add(documentReviewWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideDocumentReviewProposal))
	add(documentReviewWireAPI, http.MethodPost, a.submitDocumentReviewProposal)
}

func documentReviewScopeFrom(s wikiProposalScope) documentReviewScope {
	return documentReviewScope{Actor: s.actor, Entity: s.entity, Outlet: s.outlet}
}

func documentReviewWireInputDigest(s documentReviewScope, input documentReviewInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope documentReviewScope
		Input documentReviewInput
	}{documentReviewProposalPurpose, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitDocumentReviewProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptDocumentReviewProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptDocumentReviewProposal(w http.ResponseWriter, r *http.Request) error {
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
		Review    json.RawMessage `json:"review"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "review"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in, err := decodeDocumentReview(body.Review)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadNamedProposalSession(ctx, tx, hash, &scope, documentReviewSessionAction)
	if err != nil {
		return err
	}
	s := documentReviewScopeFrom(scope)
	if err = lockDocumentReviewScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := documentReviewWireInputDigest(s, in)
	p, err := scanDocumentReviewProposal(tx.QueryRowContext(ctx, `SELECT `+documentReviewWireColumns+` FROM mcp_document_review_proposals p
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
			FROM mcp_document_review_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := prepareDocumentReview(r, tx, s, in)
		if err != nil {
			return err
		}
		reviewDigest := prepared.digest()
		preview, err := json.Marshal(documentReviewPreview{prepared.Input, prepared.Type, prepared.Currency, prepared.TotalMinor})
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		p, err = scanDocumentReviewProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_document_review_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,request_id,input_digest,review_digest,
			review_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+documentReviewWireColumns,
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
	p.DocumentID, p.DocumentRevision = nil, nil
	jsonResponse(w, status, p.documentReviewWireReceipt)
	return nil
}

func (a *app) listDocumentReviewProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginDocumentReviewReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.document_id,p.document_revision
		FROM mcp_document_review_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []documentReviewWireReceipt{}
	for rows.Next() {
		var item documentReviewWireReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.DocumentID, &item.DocumentRevision); err != nil {
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

func lookupDocumentReviewProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte) (documentReviewWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return documentReviewWireRow{}, wikiProposalMissing
	}
	return scanDocumentReviewProposal(tx.QueryRowContext(r.Context(), `SELECT `+documentReviewWireColumns+` FROM mcp_document_review_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:]))
}

func (p documentReviewWireRow) validatedDocumentReview(s wikiProposalScope) (documentReviewSnapshot, error) {
	var preview documentReviewPreview
	if len(p.reviewDigest) != 32 || json.Unmarshal(p.preview, &preview) != nil {
		return documentReviewSnapshot{}, draftRevisionConflict
	}
	draft := documentReviewSnapshot{Scope: documentReviewScopeFrom(s), Input: preview.documentReviewInput, Type: preview.Type, Currency: preview.Currency, TotalMinor: preview.TotalMinor}
	actual := draft.digest()
	if subtle.ConstantTimeCompare(actual[:], p.reviewDigest) != 1 {
		return draft, draftRevisionConflict
	}
	return draft, nil
}

func (a *app) viewDocumentReviewProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginDocumentReviewReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupDocumentReviewProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.documentReviewProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := documentReviewWireDetails{documentReviewWireReceipt: p.documentReviewWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedDocumentReview(s)
		if err != nil {
			return err
		}
		result.Review = &documentReviewPreview{draft.Input, draft.Type, draft.Currency, draft.TotalMinor}
		result.Digest = hex.EncodeToString(p.reviewDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setDocumentReviewProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, documentID, revision *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_document_review_proposals SET status=$1,review_preview=NULL,
		decided_at=$2,document_id=$3,document_revision=$4 WHERE id=$5 AND status='pending'`, status, now, documentID, revision, id))
}

func (a *app) decideDocumentReviewProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	tx, s, login, err := a.beginDocumentReviewReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupDocumentReviewProposal(r, tx, s, login)
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
	state, err := a.documentReviewProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setDocumentReviewProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil, nil); err != nil {
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
	draft, err := p.validatedDocumentReview(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		if err := a.applyDocumentReviewApproval(r, tx, u, documentReviewScopeFrom(s), draft, confirmed); err != nil {
			return err
		}
		revision := draft.Input.Revision + 1
		p.DocumentID, p.DocumentRevision = &draft.Input.ID, &revision
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setDocumentReviewProposalTerminal(ctx, tx, p.ID, p.Status, now, p.DocumentID, p.DocumentRevision); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.documentReviewWireReceipt)
	return nil
}

func (a *app) beginDocumentReviewReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
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
		err = lockPostgresEntity(r.Context(), tx, s.entity)
		if err == nil {
			err = lockDocumentReviewScope(r.Context(), tx, documentReviewScopeFrom(s))
		}
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

func (a *app) documentReviewProposalState(ctx context.Context, tx *sql.Tx, p documentReviewWireRow) (string, error) {
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

func (a *app) pruneDocumentReviewProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_document_review_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_document_review_proposals p SET status=ended.state,review_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}
