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

const documentPointOfSaleSessionAction = "document_draft_point_of_sale_change"
const documentPointOfSaleProposalPurpose = "document_draft_point_of_sale_change.v1"

func (a *app) documentPointOfSaleProposalsEnabled() bool {
	return a.enableMCPDocumentPointOfSaleProposals
}

const documentPointOfSaleWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/document-point-of-sale-proposals"
const documentPointOfSaleWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/document-point-of-sale-proposals"

type documentPointOfSaleWireReceipt struct {
	ID               int64  `json:"id"`
	Status           string `json:"status"`
	Expires          string `json:"expires_at"`
	DocumentID       *int64 `json:"document_id,omitempty"`
	DocumentRevision *int64 `json:"document_revision,omitempty"`
}
type documentPointOfSaleWirePreview struct {
	DocumentID int64  `json:"document_id"`
	Revision   int64  `json:"expected_revision"`
	Current    string `json:"current_reference"`
	Proposed   string `json:"reference"`
}
type documentPointOfSaleWireDetails struct {
	documentPointOfSaleWireReceipt
	PointOfSaleChange *documentPointOfSaleWirePreview `json:"point_of_sale_change,omitempty"`
	Digest            string                          `json:"digest,omitempty"`
}
type documentPointOfSaleWireRow struct {
	documentPointOfSaleWireReceipt
	sessionID, expiry                       int64
	inputDigest, pointOfSaleDigest, preview []byte
}

const documentPointOfSaleWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.document_id,p.document_revision,p.input_digest,p.point_of_sale_digest,p.point_of_sale_preview`

func scanDocumentPointOfSaleProposal(row interface{ Scan(...any) error }) (documentPointOfSaleWireRow, error) {
	var p documentPointOfSaleWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.DocumentID, &p.DocumentRevision, &p.inputDigest, &p.pointOfSaleDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPDocumentPointOfSaleProposals(add func(string, string, http.HandlerFunc)) {
	if !a.documentPointOfSaleProposalsEnabled() {
		return
	}
	add(documentPointOfSaleWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listDocumentPointOfSaleProposals))
	add(documentPointOfSaleWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewDocumentPointOfSaleProposal))
	add(documentPointOfSaleWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideDocumentPointOfSaleProposal))
	add(documentPointOfSaleWireAPI, http.MethodPost, a.submitDocumentPointOfSaleProposal)
}

func documentPointOfSaleScopeFrom(s wikiProposalScope) documentReviewScope {
	return documentReviewScope{Actor: s.actor, Entity: s.entity, Outlet: s.outlet}
}

func documentPointOfSaleWireInputDigest(s documentReviewScope, input documentPointOfSaleInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope documentReviewScope
		Input documentPointOfSaleInput
	}{documentPointOfSaleProposalPurpose, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitDocumentPointOfSaleProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptDocumentPointOfSaleProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptDocumentPointOfSaleProposal(w http.ResponseWriter, r *http.Request) error {
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
		documentPointOfSaleInput
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "document_id", "expected_revision", "point_of_sale"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in := body.documentPointOfSaleInput
	if !in.valid() {
		return badInput
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadNamedProposalSession(ctx, tx, hash, &scope, documentPointOfSaleSessionAction)
	if err != nil {
		return err
	}
	s := documentPointOfSaleScopeFrom(scope)
	if err = lockScopedDocumentCapability(ctx, tx, s, draftCapability); err != nil {
		return err
	}
	inputDigest := documentPointOfSaleWireInputDigest(s, in)
	p, err := scanDocumentPointOfSaleProposal(tx.QueryRowContext(ctx, `SELECT `+documentPointOfSaleWireColumns+` FROM mcp_document_point_of_sale_proposals p
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
			FROM mcp_document_point_of_sale_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := prepareDocumentPointOfSale(r, tx, s, in)
		if err != nil {
			return err
		}
		pointOfSaleDigest := prepared.digest()
		preview, err := json.Marshal(prepared)
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		p, err = scanDocumentPointOfSaleProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_document_point_of_sale_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,request_id,input_digest,point_of_sale_digest,
			point_of_sale_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+documentPointOfSaleWireColumns,
			session, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], pointOfSaleDigest[:], preview, now.Unix(), expiry))
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
	jsonResponse(w, status, p.documentPointOfSaleWireReceipt)
	return nil
}

func (a *app) listDocumentPointOfSaleProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginDocumentPointOfSaleReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.document_id,p.document_revision
		FROM mcp_document_point_of_sale_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []documentPointOfSaleWireReceipt{}
	for rows.Next() {
		var item documentPointOfSaleWireReceipt
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

func lookupDocumentPointOfSaleProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte) (documentPointOfSaleWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return documentPointOfSaleWireRow{}, wikiProposalMissing
	}
	return scanDocumentPointOfSaleProposal(tx.QueryRowContext(r.Context(), `SELECT `+documentPointOfSaleWireColumns+` FROM mcp_document_point_of_sale_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:]))
}

func (p documentPointOfSaleWireRow) validatedDocumentPointOfSale(s wikiProposalScope) (documentPointOfSalePreview, error) {
	var draft documentPointOfSalePreview
	if len(p.pointOfSaleDigest) != 32 || json.Unmarshal(p.preview, &draft) != nil || draft.Scope != documentPointOfSaleScopeFrom(s) {
		return documentPointOfSalePreview{}, documentPointOfSaleConflict
	}
	actual := draft.digest()
	if subtle.ConstantTimeCompare(actual[:], p.pointOfSaleDigest) != 1 {
		return draft, documentPointOfSaleConflict
	}
	return draft, nil
}

func (a *app) viewDocumentPointOfSaleProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginDocumentPointOfSaleReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupDocumentPointOfSaleProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.documentPointOfSaleProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := documentPointOfSaleWireDetails{documentPointOfSaleWireReceipt: p.documentPointOfSaleWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedDocumentPointOfSale(s)
		if err != nil {
			return err
		}
		result.PointOfSaleChange = &documentPointOfSaleWirePreview{draft.Input.ID, draft.Input.Revision, draft.Current, draft.Proposed}
		result.Digest = hex.EncodeToString(p.pointOfSaleDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setDocumentPointOfSaleProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, documentID, revision *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_document_point_of_sale_proposals SET status=$1,point_of_sale_preview=NULL,
		decided_at=$2,document_id=$3,document_revision=$4 WHERE id=$5 AND status='pending'`, status, now, documentID, revision, id))
}

func (a *app) decideDocumentPointOfSaleProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	tx, s, login, err := a.beginDocumentPointOfSaleReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupDocumentPointOfSaleProposal(r, tx, s, login)
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
	state, err := a.documentPointOfSaleProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setDocumentPointOfSaleProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil, nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.pointOfSaleDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedDocumentPointOfSale(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		if err := a.applyDocumentPointOfSaleApproval(r, tx, u, documentPointOfSaleScopeFrom(s), draft, confirmed); err != nil {
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
	if err = setDocumentPointOfSaleProposalTerminal(ctx, tx, p.ID, p.Status, now, p.DocumentID, p.DocumentRevision); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.documentPointOfSaleWireReceipt)
	return nil
}

func (a *app) beginDocumentPointOfSaleReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
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
			err = lockScopedDocumentCapability(r.Context(), tx, documentPointOfSaleScopeFrom(s), draftCapability)
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

func (a *app) documentPointOfSaleProposalState(ctx context.Context, tx *sql.Tx, p documentPointOfSaleWireRow) (string, error) {
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

func (a *app) pruneDocumentPointOfSaleProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_document_point_of_sale_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_document_point_of_sale_proposals p SET status=ended.state,point_of_sale_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}
