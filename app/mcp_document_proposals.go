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

const documentProposalBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/document-draft-proposals"
const documentProposalAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/document-draft-proposals"
const documentProposalToken = "/api/v1/auth/mcp-document-proposal-token"

type documentProposalReceipt struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Expires    string `json:"expires_at"`
	DocumentID *int64 `json:"document_id,omitempty"`
}
type documentProposalDetails struct {
	documentProposalReceipt
	Draft  *documentDraftProposalPreview `json:"draft,omitempty"`
	Digest string                        `json:"digest,omitempty"`
}
type documentProposalRow struct {
	documentProposalReceipt
	tokenID, expiry                              int64
	inputDigest, reviewDigest, snapshot, preview []byte
}

const documentProposalColumns = `id,token_id,status,expires_at,document_id,input_digest,review_digest,supplier_fingerprint,draft_preview`

func scanDocumentProposal(row interface{ Scan(...any) error }) (documentProposalRow, error) {
	var p documentProposalRow
	err := row.Scan(&p.ID, &p.tokenID, &p.Status, &p.expiry, &p.DocumentID, &p.inputDigest, &p.reviewDigest, &p.snapshot, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPDocumentProposals(add func(string, string, http.HandlerFunc)) {
	if !a.enableMCPDocumentProposals {
		return
	}
	add(documentProposalToken, http.MethodPost, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
		return a.issueProposalToken(w, r, u, documentKeyPurpose)
	}))
	add(documentProposalToken+"/{token}", http.MethodDelete, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
		return a.revokeProposalToken(w, r, u, documentKeyPurpose)
	}))
	add(documentProposalBrowser, http.MethodGet, a.protectedWikiProposal(a.listDocumentProposals))
	add(documentProposalBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewDocumentProposal))
	add(documentProposalBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideDocumentProposal))
	add(documentProposalAPI, http.MethodPost, a.submitDocumentProposal)
}

func documentProposalScopeFrom(s wikiProposalScope) documentDraftProposalScope {
	return documentDraftProposalScope{Actor: s.actor, Entity: s.entity, Outlet: s.outlet}
}

func documentProposalInputDigest(s documentDraftProposalScope, input documentDraftProposalInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope documentDraftProposalScope
		Input documentDraftProposalInput
	}{documentDraftProposalKind, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitDocumentProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptDocumentProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptDocumentProposal(w http.ResponseWriter, r *http.Request) error {
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
	if len(raw) > 3<<10 {
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
	in, err := decodeDocumentDraftProposal(body.Draft)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	token, expiry, err := a.loadProposalKey(ctx, tx, hash, &scope, documentKeyPurpose)
	if err != nil {
		return err
	}
	if err = lockPostgresEntity(ctx, tx, scope.entity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return missingEntity
		}
		return err
	}
	if err = requireProposalScope(ctx, tx, scope, documentKeyPurpose); err != nil {
		return err
	}
	s := documentProposalScopeFrom(scope)
	inputDigest := documentProposalInputDigest(s, in)
	p, err := scanDocumentProposal(tx.QueryRowContext(ctx, `SELECT `+documentProposalColumns+` FROM mcp_document_draft_proposals
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
			FROM mcp_document_draft_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := prepareDocumentDraftProposal(ctx, tx, s, in)
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
		p, err = scanDocumentProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_document_draft_proposals
			(token_id,actor_id,entity_id,outlet_id,request_id,input_digest,review_digest,
			supplier_fingerprint,draft_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10,$11) RETURNING `+documentProposalColumns,
			token, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], reviewDigest[:], prepared.SupplierFingerprint[:], preview, now.Unix(), expiry))
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
	p.DocumentID = nil
	jsonResponse(w, status, p.documentProposalReceipt)
	return nil
}

func (a *app) beginTypedProposalReview(r *http.Request, u user, purpose string, decision bool) (*sql.Tx, wikiProposalScope, []byte, error) {
	s, err := wikiProposalPath(r)
	if err != nil {
		return nil, s, nil, err
	}
	s.actor = u.id
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil, s, nil, err
	}
	password, err := lockWikiProposalBrowser(r, tx, u.id)
	if err == nil && decision {
		// Acquire the native exclusive entity lock before taking any shared
		// scope locks. Native document writes use this same lock.
		err = lockPostgresEntity(r.Context(), tx, s.entity)
		if errors.Is(err, sql.ErrNoRows) {
			err = missingEntity
		}
	}
	if err == nil {
		err = requireProposalScope(r.Context(), tx, s, purpose)
	}
	if err != nil {
		tx.Rollback()
		return nil, s, nil, err
	}
	return tx, s, password, nil
}

func (a *app) listDocumentProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, password, err := a.beginTypedProposalReview(r, u, documentKeyPurpose, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND (t.revoked_at IS NOT NULL OR t.password_digest<>$5) THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.document_id
		FROM mcp_document_draft_proposals p JOIN mcp_wiki_proposal_tokens t ON t.id=p.token_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL AND t.password_digest=$5) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), password, now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []documentProposalReceipt{}
	for rows.Next() {
		var item documentProposalReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.DocumentID); err != nil {
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

func lookupDocumentProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope) (documentProposalRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return documentProposalRow{}, wikiProposalMissing
	}
	return scanDocumentProposal(tx.QueryRowContext(r.Context(), `SELECT `+documentProposalColumns+` FROM mcp_document_draft_proposals
		WHERE id=$1 AND actor_id=$2 AND entity_id=$3 AND outlet_id=$4 FOR UPDATE`, id, s.actor, s.entity, s.outlet))
}

func (a *app) typedProposalState(ctx context.Context, tx *sql.Tx, status string, expiry, tokenID int64, password []byte) (string, error) {
	if status != "pending" {
		return status, nil
	}
	if expiry <= a.tokenTime().Unix() {
		return "expired", nil
	}
	valid, err := wikiProposalTokenStatus(ctx, tx, tokenID, password)
	if err != nil {
		return "", err
	}
	if !valid {
		return "revoked", nil
	}
	return "pending", nil
}

func (p documentProposalRow) validatedDraft(s wikiProposalScope) (documentDraftProposal, error) {
	var draft documentDraftProposal
	draft.Scope = documentProposalScopeFrom(s)
	if len(p.snapshot) != 32 || len(p.reviewDigest) != 32 || json.Unmarshal(p.preview, &draft.Preview) != nil {
		return draft, draftProposalStale
	}
	copy(draft.SupplierFingerprint[:], p.snapshot)
	actual := draft.digest()
	if subtle.ConstantTimeCompare(actual[:], p.reviewDigest) != 1 {
		return draft, draftProposalStale
	}
	return draft, nil
}

func (a *app) viewDocumentProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, password, err := a.beginTypedProposalReview(r, u, documentKeyPurpose, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupDocumentProposal(r, tx, s)
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
	result := documentProposalDetails{documentProposalReceipt: p.documentProposalReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedDraft(s)
		if err != nil {
			return err
		}
		result.Draft = &draft.Preview
		result.Digest = hex.EncodeToString(p.reviewDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setDocumentProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, document *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_document_draft_proposals SET status=$1,draft_preview=NULL,
		supplier_fingerprint=NULL,decided_at=$2,document_id=$3 WHERE id=$4 AND status='pending'`, status, now, document, id))
}

func (a *app) decideDocumentProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	tx, s, password, err := a.beginTypedProposalReview(r, u, documentKeyPurpose, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupDocumentProposal(r, tx, s)
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
		if err = setDocumentProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil); err != nil {
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
	draft, err := p.validatedDraft(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		id, err := a.applyDocumentDraftProposal(r, tx, u, documentProposalScopeFrom(s), draft, confirmed)
		if err != nil {
			return err
		}
		p.DocumentID = &id
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setDocumentProposalTerminal(ctx, tx, p.ID, p.Status, now, p.DocumentID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.documentProposalReceipt)
	return nil
}
