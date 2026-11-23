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

const documentNumberSessionAction = "document_draft_number_change"
const documentNumberProposalPurpose = "document_draft_number_change.v1"

func (a *app) documentNumberProposalsEnabled() bool { return a.enableMCPDocumentNumberProposals }

const documentNumberWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/document-number-proposals"
const documentNumberWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/document-number-proposals"

type documentNumberWireReceipt struct {
	ID               int64  `json:"id"`
	Status           string `json:"status"`
	Expires          string `json:"expires_at"`
	DocumentID       *int64 `json:"document_id,omitempty"`
	DocumentRevision *int64 `json:"document_revision,omitempty"`
}
type documentNumberWirePreview struct {
	DocumentID int64  `json:"document_id"`
	Revision   int64  `json:"expected_revision"`
	Current    string `json:"current_reference"`
	Proposed   string `json:"reference"`
}
type documentNumberWireDetails struct {
	documentNumberWireReceipt
	NumberChange *documentNumberWirePreview `json:"number_change,omitempty"`
	Digest       string                     `json:"digest,omitempty"`
}
type documentNumberWireRow struct {
	documentNumberWireReceipt
	sessionID, expiry                  int64
	inputDigest, numberDigest, preview []byte
}

const documentNumberWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.document_id,p.document_revision,p.input_digest,p.number_digest,p.number_preview`

func scanDocumentNumberProposal(row interface{ Scan(...any) error }) (documentNumberWireRow, error) {
	var p documentNumberWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.DocumentID, &p.DocumentRevision, &p.inputDigest, &p.numberDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPDocumentNumberProposals(add func(string, string, http.HandlerFunc)) {
	if !a.documentNumberProposalsEnabled() {
		return
	}
	add(documentNumberWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listDocumentNumberProposals))
	add(documentNumberWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewDocumentNumberProposal))
	add(documentNumberWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideDocumentNumberProposal))
	add(documentNumberWireAPI, http.MethodPost, a.submitDocumentNumberProposal)
}

func documentNumberScopeFrom(s wikiProposalScope) documentReviewScope {
	return documentReviewScope{Actor: s.actor, Entity: s.entity, Outlet: s.outlet}
}

func documentNumberWireInputDigest(s documentReviewScope, input documentNumberInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope documentReviewScope
		Input documentNumberInput
	}{documentNumberProposalPurpose, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitDocumentNumberProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptDocumentNumberProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptDocumentNumberProposal(w http.ResponseWriter, r *http.Request) error {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.Header.Values("Cookie")) != 0 {
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
	if len(raw) > 4096 {
		return badInput
	}
	var body struct {
		RequestID string `json:"request_id"`
		documentNumberInput
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "document_id", "expected_revision", "number"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in := body.documentNumberInput
	if !in.valid() {
		return badInput
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadNamedProposalSession(ctx, tx, hash, &scope, documentNumberSessionAction)
	if err != nil {
		return err
	}
	s := documentNumberScopeFrom(scope)
	if err = lockScopedDocumentCapability(ctx, tx, s, draftCapability); err != nil {
		return err
	}
	inputDigest := documentNumberWireInputDigest(s, in)
	p, err := scanDocumentNumberProposal(tx.QueryRowContext(ctx, `SELECT `+documentNumberWireColumns+` FROM mcp_document_number_proposals p
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
			FROM mcp_document_number_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := prepareDocumentNumber(r, tx, s, in)
		if err != nil {
			return err
		}
		numberDigest := prepared.digest()
		preview, err := json.Marshal(prepared)
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		p, err = scanDocumentNumberProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_document_number_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,request_id,input_digest,number_digest,
			number_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+documentNumberWireColumns,
			session, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], numberDigest[:], preview, now.Unix(), expiry))
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
	jsonResponse(w, status, p.documentNumberWireReceipt)
	return nil
}

func (a *app) listDocumentNumberProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginDocumentNumberReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.document_id,p.document_revision
		FROM mcp_document_number_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []documentNumberWireReceipt{}
	for rows.Next() {
		var item documentNumberWireReceipt
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

func lookupDocumentNumberProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte) (documentNumberWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return documentNumberWireRow{}, wikiProposalMissing
	}
	return scanDocumentNumberProposal(tx.QueryRowContext(r.Context(), `SELECT `+documentNumberWireColumns+` FROM mcp_document_number_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:]))
}

func (p documentNumberWireRow) validatedDocumentNumber(s wikiProposalScope) (documentNumberPreview, error) {
	var draft documentNumberPreview
	if len(p.numberDigest) != 32 || json.Unmarshal(p.preview, &draft) != nil || draft.Scope != documentNumberScopeFrom(s) {
		return documentNumberPreview{}, documentNumberConflict
	}
	actual := draft.digest()
	if subtle.ConstantTimeCompare(actual[:], p.numberDigest) != 1 {
		return draft, documentNumberConflict
	}
	return draft, nil
}

func (a *app) viewDocumentNumberProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginDocumentNumberReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupDocumentNumberProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.documentNumberProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := documentNumberWireDetails{documentNumberWireReceipt: p.documentNumberWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedDocumentNumber(s)
		if err != nil {
			return err
		}
		result.NumberChange = &documentNumberWirePreview{draft.Input.ID, draft.Input.Revision, draft.Current, draft.Proposed}
		result.Digest = hex.EncodeToString(p.numberDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setDocumentNumberProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, documentID, revision *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_document_number_proposals SET status=$1,number_preview=NULL,
		decided_at=$2,document_id=$3,document_revision=$4 WHERE id=$5 AND status='pending'`, status, now, documentID, revision, id))
}

func (a *app) decideDocumentNumberProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	tx, s, login, err := a.beginDocumentNumberReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupDocumentNumberProposal(r, tx, s, login)
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
	state, err := a.documentNumberProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setDocumentNumberProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil, nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.numberDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedDocumentNumber(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		if err := a.applyDocumentNumberApproval(r, tx, u, documentNumberScopeFrom(s), draft, confirmed); err != nil {
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
	if err = setDocumentNumberProposalTerminal(ctx, tx, p.ID, p.Status, now, p.DocumentID, p.DocumentRevision); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.documentNumberWireReceipt)
	return nil
}

func (a *app) beginDocumentNumberReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
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
			err = lockScopedDocumentCapability(r.Context(), tx, documentNumberScopeFrom(s), draftCapability)
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

func (a *app) documentNumberProposalState(ctx context.Context, tx *sql.Tx, p documentNumberWireRow) (string, error) {
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

func (a *app) pruneDocumentNumberProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_document_number_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_document_number_proposals p SET status=ended.state,number_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}
