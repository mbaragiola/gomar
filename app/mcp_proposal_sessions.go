package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"time"
)

const posRenameSessionAction = "pos_product_rename"
const proposalSessionsAuthPath = "/api/v1/auth/mcp-proposal-sessions"
const proposalSessionListPath = "/api/v1/entities/{entity}/outlets/{outlet}/proposal-sessions"

func (a *app) posRenameProposalsEnabled() bool {
	return a.enableMCPProposalSessions && a.enablePOS
}

// The caller holds the shared actor lock used by both issuance paths.
func checkProposalCredentialQuota(ctx context.Context, tx *sql.Tx, actor int64, purpose string, now time.Time) error {
	var active, perAction, recent int64
	err := tx.QueryRowContext(ctx, `WITH legacy AS (
		SELECT expires_at,revoked_at,purpose FROM mcp_wiki_proposal_tokens
		WHERE actor_id=$1 AND expires_at>$3
	), named AS (
		SELECT k.id,k.expires_at,k.revoked_at,
			EXISTS(SELECT 1 FROM sessions s WHERE s.token_hash=k.login_hash AND s.user_id=k.actor_id AND s.expires>$5) AS live_login
		FROM mcp_proposal_sessions k WHERE k.actor_id=$1 AND k.expires_at>$3
	) SELECT
		(SELECT count(*) FROM legacy WHERE expires_at>$2 AND revoked_at IS NULL) +
		(SELECT count(*) FROM named WHERE expires_at>$2 AND revoked_at IS NULL AND live_login),
		(SELECT count(*) FROM legacy WHERE expires_at>$2 AND revoked_at IS NULL AND purpose=$4) +
		(SELECT count(*) FROM named n JOIN mcp_proposal_session_actions a ON a.session_id=n.id
			WHERE n.expires_at>$2 AND n.revoked_at IS NULL AND n.live_login AND a.action=$4),
		(SELECT count(*) FROM legacy) + (SELECT count(*) FROM named)`,
		actor, now.Unix(), now.Add(-wikiProposalRetention).Unix(), purpose, time.Now().Unix()).
		Scan(&active, &perAction, &recent)
	if err != nil {
		return err
	}
	if active >= 4 || perAction >= 3 || recent >= 48 {
		return wikiProposalLimit
	}
	return nil
}

type namedProposalSession struct {
	ID      int64    `json:"id"`
	Expires string   `json:"expires_at"`
	Actions []string `json:"actions"`
}

func proposalLoginHash(r *http.Request) ([32]byte, error) {
	key := cookieValue(r, "sessionid")
	if len(key) != 32 {
		return [32]byte{}, unauthenticated
	}
	return sha256.Sum256([]byte(key)), nil
}

func (a *app) registerNamedProposalSessions(add func(string, string, http.HandlerFunc)) {
	if !a.posRenameProposalsEnabled() && !a.documentReviewProposalsEnabled() && !a.reconciliationDraftProposalsEnabled() && !a.documentEditProposalsEnabled() {
		return
	}
	add(proposalSessionsAuthPath, http.MethodPost, a.protectedWikiProposal(a.issueNamedProposalSession))
	add(proposalSessionsAuthPath+"/{session}", http.MethodDelete, a.protectedWikiProposal(a.revokeNamedProposalSession))
	add(proposalSessionListPath, http.MethodGet, a.protectedWikiProposal(a.listNamedProposalSessions))
}

func (a *app) issueNamedProposalSession(w http.ResponseWriter, r *http.Request, u user) error {
	raw, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	var input struct {
		Entity  int64    `json:"entity_id"`
		Outlet  int64    `json:"outlet_id"`
		Actions []string `json:"actions"`
	}
	if err = decodeWikiProposalJSON(raw, &input, "entity_id", "outlet_id", "actions"); err != nil {
		return err
	}
	// No wildcard, configurable grant names, or empty action sets.
	if input.Entity <= 0 || input.Outlet <= 0 || len(input.Actions) != 1 {
		return badInput
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	password, err := lockWikiProposalBrowser(r, tx, u.id)
	if err != nil {
		return err
	}
	login, err := proposalLoginHash(r)
	if err != nil {
		return err
	}
	action := input.Actions[0]
	switch action {
	case posRenameSessionAction:
		if !a.posRenameProposalsEnabled() {
			return badInput
		}
		_, err = a.lockPOSRenameScope(ctx, tx, posRenameScope{u.id, input.Entity, input.Outlet})
	case documentReviewSessionAction:
		if !a.documentReviewProposalsEnabled() {
			return badInput
		}
		err = lockDocumentReviewScope(ctx, tx, documentReviewScope{u.id, input.Entity, input.Outlet})
	case reconciliationDraftSessionAction:
		if !a.reconciliationDraftProposalsEnabled() {
			return badInput
		}
		_, err = lockReconciliationDraftScope(ctx, tx, reconciliationDraftScope{u.id, input.Entity, input.Outlet})
	case documentEditSessionAction:
		if !a.documentEditProposalsEnabled() {
			return badInput
		}
		err = lockScopedDocumentCapability(ctx, tx, documentReviewScope{u.id, input.Entity, input.Outlet}, draftCapability)
	default:
		return badInput
	}
	if err != nil {
		return err
	}
	var loginExpiry int64
	if err = tx.QueryRowContext(ctx, `SELECT expires FROM sessions WHERE token_hash=$1 AND user_id=$2`, login[:], u.id).Scan(&loginExpiry); err != nil {
		return err
	}
	now := a.tokenTime()
	deadline := min(loginExpiry, now.Add(wikiProposalLifetime).Unix())
	if deadline <= now.Unix() || loginExpiry <= time.Now().Unix() {
		return unauthenticated
	}
	if err = checkProposalCredentialQuota(ctx, tx, u.id, action, now); err != nil {
		return err
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	hash := sha256.Sum256(secret)
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO mcp_proposal_sessions(actor_id,entity_id,outlet_id,login_hash,bearer_hash,password_digest,issued_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		u.id, input.Entity, input.Outlet, login[:], hash[:], password, now.Unix(), deadline).Scan(&id)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mcp_proposal_session_actions(session_id,action) VALUES($1,$2)`, id, action); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusCreated, struct {
		namedProposalSession
		Token string `json:"token"`
	}{namedProposalSession{id, time.Unix(deadline, 0).UTC().Format(time.RFC3339), []string{action}}, base64.RawURLEncoding.EncodeToString(secret)})
	return nil
}

func (a *app) revokeNamedProposalSession(w http.ResponseWriter, r *http.Request, u user) error {
	id, err := pathID(r, "session")
	if err != nil || id <= 0 {
		return badInput
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if _, err = tx.ExecContext(ctx, `UPDATE mcp_proposal_sessions SET revoked_at=$1 WHERE id=$2 AND actor_id=$3 AND revoked_at IS NULL`, now, id, u.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mcp_pos_rename_proposals SET status='revoked',rename_preview=NULL,decided_at=$1
		WHERE session_id=$2 AND actor_id=$3 AND status='pending'`, now, id, u.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mcp_reconciliation_draft_proposals SET status='revoked',draft_preview=NULL,decided_at=$1
 WHERE session_id=$2 AND actor_id=$3 AND status='pending'`, now, id, u.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mcp_document_edit_proposals SET status='revoked',edit_preview=NULL,decided_at=$1
 WHERE session_id=$2 AND actor_id=$3 AND status='pending'`, now, id, u.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mcp_document_review_proposals SET status='revoked',review_preview=NULL,decided_at=$1
  WHERE session_id=$2 AND actor_id=$3 AND status='pending'`, now, id, u.id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *app) listNamedProposalSessions(w http.ResponseWriter, r *http.Request, u user) error {
	s, err := wikiProposalPath(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	digest, err := lockWikiProposalBrowser(r, tx, u.id)
	if err != nil {
		return err
	}
	// Owner-only credential management survives a role downgrade. Never
	// enumerate other people's sessions or expose their stored scopes.
	rows, err := tx.QueryContext(ctx, `SELECT k.id,k.expires_at,a.action FROM mcp_proposal_sessions k
		JOIN mcp_proposal_session_actions a ON a.session_id=k.id
		JOIN sessions s ON s.token_hash=k.login_hash AND s.user_id=k.actor_id AND s.password_digest=k.password_digest
		WHERE k.actor_id=$1 AND k.entity_id=$2 AND k.outlet_id=$3 AND k.expires_at>$4 AND k.revoked_at IS NULL
			AND k.password_digest=$5 AND s.expires>$6 ORDER BY k.id DESC LIMIT 4`,
		u.id, s.entity, s.outlet, a.tokenTime().Unix(), digest, time.Now().Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	result := []namedProposalSession{}
	for rows.Next() {
		var x namedProposalSession
		var expiry int64
		var action string
		if err = rows.Scan(&x.ID, &expiry, &action); err != nil {
			return err
		}
		x.Expires = time.Unix(expiry, 0).UTC().Format(time.RFC3339)
		x.Actions = []string{action}
		result = append(result, x)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, map[string]any{"sessions": result})
	return nil
}

func (a *app) loadPOSRenameSession(ctx context.Context, tx *sql.Tx, hash []byte, s *wikiProposalScope) (int64, int64, error) {
	return a.loadNamedProposalSession(ctx, tx, hash, s, posRenameSessionAction)
}
func (a *app) loadNamedProposalSession(ctx context.Context, tx *sql.Tx, hash []byte, s *wikiProposalScope, action string) (id, expiry int64, err error) {
	err = tx.QueryRowContext(ctx, `SELECT actor_id FROM mcp_proposal_sessions WHERE bearer_hash=$1`, hash).Scan(&s.actor)
	if errors.Is(err, sql.ErrNoRows) {
		err = unauthenticated
	}
	if err != nil {
		return
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT password FROM users WHERE id=$1 AND active FOR UPDATE`, s.actor).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = unauthenticated
	}
	if err != nil {
		return
	}
	var entity, outlet int64
	var saved []byte
	err = tx.QueryRowContext(ctx, `SELECT k.id,k.expires_at,k.entity_id,k.outlet_id,k.password_digest
		FROM mcp_proposal_sessions k
		JOIN mcp_proposal_session_actions a ON a.session_id=k.id AND a.action=$4
		JOIN sessions l ON l.token_hash=k.login_hash AND l.user_id=k.actor_id AND l.password_digest=k.password_digest
		WHERE k.bearer_hash=$1 AND k.actor_id=$2 AND k.issued_at<=$3 AND k.expires_at>$3 AND k.revoked_at IS NULL
			AND l.expires>$5 FOR SHARE OF k,l`, hash, s.actor, a.tokenTime().Unix(), action, time.Now().Unix()).
		Scan(&id, &expiry, &entity, &outlet, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		err = unauthenticated
	}
	if err != nil {
		return
	}
	password := sha256.Sum256([]byte(raw))
	switch {
	case subtle.ConstantTimeCompare(saved, password[:]) != 1:
		err = unauthenticated
	case entity != s.entity:
		err = missingEntity
	case outlet != s.outlet:
		err = missingOutlet
	}
	return
}
