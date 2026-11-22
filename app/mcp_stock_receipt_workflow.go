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

const stockReceiptWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/stock-receipt-proposals"
const stockReceiptWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/stock-receipt-proposals"
const stockReceiptWireToken = "/api/v1/auth/mcp-stock-receipt-proposal-token"

type stockReceiptWireReceipt struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Expires    string `json:"expires_at"`
	MovementID *int64 `json:"movement_id,omitempty"`
}
type stockReceiptWireDetails struct {
	stockReceiptWireReceipt
	Receipt *stockReceiptPreview `json:"receipt,omitempty"`
	Digest  string               `json:"digest,omitempty"`
}
type stockReceiptWireRow struct {
	stockReceiptWireReceipt
	tokenID, expiry                    int64
	requestID                          string
	inputDigest, reviewDigest, preview []byte
}

const stockReceiptWireColumns = `id,token_id,status,expires_at,movement_id,request_id::text,input_digest,review_digest,receipt_preview`

func scanStockReceiptProposal(row interface{ Scan(...any) error }) (stockReceiptWireRow, error) {
	var p stockReceiptWireRow
	err := row.Scan(&p.ID, &p.tokenID, &p.Status, &p.expiry, &p.MovementID, &p.requestID, &p.inputDigest, &p.reviewDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) stockReceiptProposalsEnabled() bool {
	return a.enableMCPStockReceiptProposals && a.enablePOS && a.enableStock
}

func (a *app) registerMCPStockReceiptProposals(add func(string, string, http.HandlerFunc)) {
	if !a.stockReceiptProposalsEnabled() {
		return
	}
	add(stockReceiptWireToken, http.MethodPost, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
		return a.issueProposalToken(w, r, u, stockReceiptKeyPurpose)
	}))
	add(stockReceiptWireToken+"/{token}", http.MethodDelete, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
		return a.revokeProposalToken(w, r, u, stockReceiptKeyPurpose)
	}))
	add(stockReceiptWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listStockReceiptProposals))
	add(stockReceiptWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewStockReceiptProposal))
	add(stockReceiptWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideStockReceiptProposal))
	add(stockReceiptWireAPI, http.MethodPost, a.submitStockReceiptProposal)
}

func stockReceiptScopeFrom(s wikiProposalScope) stockReceiptScope {
	return stockReceiptScope{Actor: s.actor, Entity: s.entity, Outlet: s.outlet}
}

func stockReceiptWireInputDigest(s stockReceiptScope, input stockReceiptInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope stockReceiptScope
		Input stockReceiptInput
	}{stockReceiptPurpose, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitStockReceiptProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptStockReceiptProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptStockReceiptProposal(w http.ResponseWriter, r *http.Request) error {
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
	if len(raw) > 1024 {
		return badInput
	}
	var body struct {
		RequestID string          `json:"request_id"`
		Receipt   json.RawMessage `json:"receipt"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "receipt"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in, err := decodeStockReceiptProposal(body.Receipt)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	token, expiry, err := a.loadProposalKey(ctx, tx, hash, &scope, stockReceiptKeyPurpose)
	if err != nil {
		return err
	}
	s := stockReceiptScopeFrom(scope)
	if err = a.lockStockReceiptScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := stockReceiptWireInputDigest(s, in)
	p, err := scanStockReceiptProposal(tx.QueryRowContext(ctx, `SELECT `+stockReceiptWireColumns+` FROM mcp_stock_receipt_proposals
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
			FROM mcp_stock_receipt_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := a.prepareStockReceiptProposal(ctx, tx, s, in, body.RequestID)
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
		p, err = scanStockReceiptProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_stock_receipt_proposals
			(token_id,actor_id,entity_id,outlet_id,request_id,input_digest,review_digest,
			receipt_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+stockReceiptWireColumns,
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
	p.MovementID = nil
	jsonResponse(w, status, p.stockReceiptWireReceipt)
	return nil
}

func (a *app) listStockReceiptProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, password, err := a.beginTypedProposalReview(r, u, stockReceiptKeyPurpose, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND (t.revoked_at IS NOT NULL OR t.password_digest<>$5) THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.movement_id
		FROM mcp_stock_receipt_proposals p JOIN mcp_wiki_proposal_tokens t ON t.id=p.token_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL AND t.password_digest=$5) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), password, now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []stockReceiptWireReceipt{}
	for rows.Next() {
		var item stockReceiptWireReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.MovementID); err != nil {
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

func lookupStockReceiptProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope) (stockReceiptWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return stockReceiptWireRow{}, wikiProposalMissing
	}
	return scanStockReceiptProposal(tx.QueryRowContext(r.Context(), `SELECT `+stockReceiptWireColumns+` FROM mcp_stock_receipt_proposals
		WHERE id=$1 AND actor_id=$2 AND entity_id=$3 AND outlet_id=$4 FOR UPDATE`, id, s.actor, s.entity, s.outlet))
}

func (p stockReceiptWireRow) validatedStockReceipt(s wikiProposalScope) (stockReceiptProposal, error) {
	var draft stockReceiptProposal
	draft.Scope = stockReceiptScopeFrom(s)
	draft.RequestID = p.requestID
	if len(p.reviewDigest) != 32 || json.Unmarshal(p.preview, &draft.Preview) != nil {
		return draft, stockReceiptStale
	}
	actual := draft.digest()
	if subtle.ConstantTimeCompare(actual[:], p.reviewDigest) != 1 {
		return draft, stockReceiptStale
	}
	return draft, nil
}

func (a *app) viewStockReceiptProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, password, err := a.beginTypedProposalReview(r, u, stockReceiptKeyPurpose, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupStockReceiptProposal(r, tx, s)
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
	result := stockReceiptWireDetails{stockReceiptWireReceipt: p.stockReceiptWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedStockReceipt(s)
		if err != nil {
			return err
		}
		result.Receipt = &draft.Preview
		result.Digest = hex.EncodeToString(p.reviewDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setStockReceiptProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, movementID *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_stock_receipt_proposals SET status=$1,receipt_preview=NULL,
		decided_at=$2,movement_id=$3 WHERE id=$4 AND status='pending'`, status, now, movementID, id))
}

func (a *app) decideStockReceiptProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	tx, s, password, err := a.beginTypedProposalReview(r, u, stockReceiptKeyPurpose, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupStockReceiptProposal(r, tx, s)
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
		if err = setStockReceiptProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil); err != nil {
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
	draft, err := p.validatedStockReceipt(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		movement, err := a.applyStockReceiptProposal(r, tx, u, stockReceiptScopeFrom(s), draft, confirmed, a.tokenTime())
		if err != nil {
			return err
		}
		p.MovementID = &movement.ID
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setStockReceiptProposalTerminal(ctx, tx, p.ID, p.Status, now, p.MovementID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.stockReceiptWireReceipt)
	return nil
}
