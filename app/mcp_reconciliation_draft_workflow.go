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

const reconciliationDraftSessionAction = "reconciliation_draft_create"
const reconciliationDraftProposalPurpose = "reconciliation_draft.v1"

func (a *app) reconciliationDraftProposalsEnabled() bool {
	return a.enableMCPReconciliationDraftProposals
}

func decodeReconciliationDraft(raw []byte) (reconciliationDraftInput, error) {
	var wire struct {
		MatchReference *bool `json:"match_reference"`
	}
	if err := decodeWikiProposalJSON(raw, &wire, "match_reference"); err != nil {
		return reconciliationDraftInput{}, err
	}
	if wire.MatchReference == nil {
		return reconciliationDraftInput{}, badInput
	}
	return reconciliationDraftInput{MatchReference: *wire.MatchReference}, nil
}

const reconciliationDraftWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/reconciliation-draft-proposals"
const reconciliationDraftWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/reconciliation-draft-proposals"

type reconciliationDraftWireReceipt struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Expires string `json:"expires_at"`
	RunID   *int64 `json:"run_id,omitempty"`
}
type reconciliationDraftWireDetails struct {
	reconciliationDraftWireReceipt
	Draft  *reconciliationDraftInput `json:"draft,omitempty"`
	Digest string                    `json:"digest,omitempty"`
}
type reconciliationDraftWireRow struct {
	reconciliationDraftWireReceipt
	sessionID, expiry                 int64
	inputDigest, draftDigest, preview []byte
}

const reconciliationDraftWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.run_id,p.input_digest,p.draft_digest,p.draft_preview`

func scanReconciliationDraftProposal(row interface{ Scan(...any) error }) (reconciliationDraftWireRow, error) {
	var p reconciliationDraftWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.RunID, &p.inputDigest, &p.draftDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPReconciliationDraftProposals(add func(string, string, http.HandlerFunc)) {
	if !a.reconciliationDraftProposalsEnabled() {
		return
	}
	add(reconciliationDraftWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listReconciliationDraftProposals))
	add(reconciliationDraftWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewReconciliationDraftProposal))
	add(reconciliationDraftWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideReconciliationDraftProposal))
	add(reconciliationDraftWireAPI, http.MethodPost, a.submitReconciliationDraftProposal)
}

func reconciliationDraftScopeFrom(s wikiProposalScope) reconciliationDraftScope {
	return reconciliationDraftScope{Actor: s.actor, Entity: s.entity, Outlet: s.outlet}
}

func reconciliationDraftWireInputDigest(s reconciliationDraftScope, input reconciliationDraftInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope reconciliationDraftScope
		Input reconciliationDraftInput
	}{reconciliationDraftProposalPurpose, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitReconciliationDraftProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptReconciliationDraftProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptReconciliationDraftProposal(w http.ResponseWriter, r *http.Request) error {
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
	if len(raw) > 256 {
		return badInput
	}
	var body struct {
		RequestID string          `json:"request_id"`
		Draft     json.RawMessage `json:"draft"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "draft"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in, err := decodeReconciliationDraft(body.Draft)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadNamedProposalSession(ctx, tx, hash, &scope, reconciliationDraftSessionAction)
	if err != nil {
		return err
	}
	s := reconciliationDraftScopeFrom(scope)
	if _, err = lockReconciliationDraftScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := reconciliationDraftWireInputDigest(s, in)
	p, err := scanReconciliationDraftProposal(tx.QueryRowContext(ctx, `SELECT `+reconciliationDraftWireColumns+` FROM mcp_reconciliation_draft_proposals p
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
			FROM mcp_reconciliation_draft_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := prepareReconciliationDraft(tx, s, in)
		if err != nil {
			return err
		}
		draftDigest := prepared.digest()
		preview, err := json.Marshal(prepared.Input)
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		p, err = scanReconciliationDraftProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_reconciliation_draft_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,request_id,input_digest,draft_digest,
			draft_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+reconciliationDraftWireColumns,
			session, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], draftDigest[:], preview, now.Unix(), expiry))
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
	p.RunID = nil
	jsonResponse(w, status, p.reconciliationDraftWireReceipt)
	return nil
}

func (a *app) listReconciliationDraftProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginReconciliationDraftReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.run_id
		FROM mcp_reconciliation_draft_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []reconciliationDraftWireReceipt{}
	for rows.Next() {
		var item reconciliationDraftWireReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.RunID); err != nil {
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

func lookupReconciliationDraftProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte) (reconciliationDraftWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return reconciliationDraftWireRow{}, wikiProposalMissing
	}
	return scanReconciliationDraftProposal(tx.QueryRowContext(r.Context(), `SELECT `+reconciliationDraftWireColumns+` FROM mcp_reconciliation_draft_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:]))
}

func (p reconciliationDraftWireRow) validatedReconciliationDraft(s wikiProposalScope) (reconciliationDraftPreview, error) {
	if len(p.draftDigest) != 32 {
		return reconciliationDraftPreview{}, wikiProposalConflict
	}
	preview, err := decodeReconciliationDraft(p.preview)
	if err != nil {
		return reconciliationDraftPreview{}, wikiProposalConflict
	}
	draft := reconciliationDraftPreview{Scope: reconciliationDraftScopeFrom(s), Input: preview}
	actual := draft.digest()
	if subtle.ConstantTimeCompare(actual[:], p.draftDigest) != 1 {
		return draft, wikiProposalConflict
	}
	return draft, nil
}

func (a *app) viewReconciliationDraftProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginReconciliationDraftReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupReconciliationDraftProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.reconciliationDraftProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := reconciliationDraftWireDetails{reconciliationDraftWireReceipt: p.reconciliationDraftWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedReconciliationDraft(s)
		if err != nil {
			return err
		}
		result.Draft = &draft.Input
		result.Digest = hex.EncodeToString(p.draftDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setReconciliationDraftProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, runID *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_reconciliation_draft_proposals SET status=$1,draft_preview=NULL,
		decided_at=$2,run_id=$3 WHERE id=$4 AND status='pending'`, status, now, runID, id))
}

func (a *app) decideReconciliationDraftProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	tx, s, login, err := a.beginReconciliationDraftReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupReconciliationDraftProposal(r, tx, s, login)
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
	state, err := a.reconciliationDraftProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setReconciliationDraftProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.draftDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedReconciliationDraft(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		run, err := a.applyReconciliationDraftApproval(ctx, tx, u, reconciliationDraftScopeFrom(s), draft, confirmed)
		if err != nil {
			return err
		}
		p.RunID = &run.ID
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setReconciliationDraftProposalTerminal(ctx, tx, p.ID, p.Status, now, p.RunID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.reconciliationDraftWireReceipt)
	return nil
}

func (a *app) beginReconciliationDraftReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
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
			_, err = lockReconciliationDraftScope(r.Context(), tx, reconciliationDraftScopeFrom(s))
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

func (a *app) reconciliationDraftProposalState(ctx context.Context, tx *sql.Tx, p reconciliationDraftWireRow) (string, error) {
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

func (a *app) pruneReconciliationDraftProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_reconciliation_draft_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_reconciliation_draft_proposals p SET status=ended.state,draft_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}
