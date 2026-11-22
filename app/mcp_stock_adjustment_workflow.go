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

const stockWasteSessionAction = "stock_waste_record"
const stockPhysicalCountSessionAction = "stock_physical_count"

func (op stockAdjustmentOperation) action() string {
	if op == stockWaste {
		return stockWasteSessionAction
	}
	if op == stockPhysicalCount {
		return stockPhysicalCountSessionAction
	}
	return ""
}

func (a *app) stockAdjustmentProposalsEnabled() bool {
	return a.enableMCPStockAdjustmentProposals && a.enablePOS && a.enableStock
}

type stockAdjustmentWireReceipt struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Expires    string `json:"expires_at"`
	MovementID *int64 `json:"movement_id,omitempty"`
}
type stockAdjustmentWireDetails struct {
	stockAdjustmentWireReceipt
	Adjustment *stockAdjustmentPreview `json:"adjustment,omitempty"`
	Digest     string                  `json:"digest,omitempty"`
}
type stockAdjustmentWireRow struct {
	stockAdjustmentWireReceipt
	sessionID, expiry                      int64
	requestID                              string
	inputDigest, adjustmentDigest, preview []byte
}

const stockAdjustmentWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.movement_id,p.request_id::text,p.input_digest,p.adjustment_digest,p.adjustment_preview`

func scanStockAdjustmentProposal(row interface{ Scan(...any) error }) (stockAdjustmentWireRow, error) {
	var p stockAdjustmentWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.MovementID, &p.requestID, &p.inputDigest, &p.adjustmentDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPStockAdjustmentProposals(add func(string, string, http.HandlerFunc)) {
	if !a.stockAdjustmentProposalsEnabled() {
		return
	}
	bind := func(slug string, op stockAdjustmentOperation) {
		browser := "/api/v1/entities/{entity}/outlets/{outlet}/" + slug
		api := "/api/v1/mcp/entities/{entity}/outlets/{outlet}/" + slug
		add(browser, http.MethodGet, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
			return a.listStockAdjustmentProposals(w, r, u, op)
		}))
		add(browser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
			return a.viewStockAdjustmentProposal(w, r, u, op)
		}))
		add(browser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
			return a.decideStockAdjustmentProposal(w, r, u, op)
		}))
		add(api, http.MethodPost, func(w http.ResponseWriter, r *http.Request) { a.submitStockAdjustmentProposal(w, r, op) })
	}
	bind("stock-waste-proposals", stockWaste)
	bind("stock-physical-count-proposals", stockPhysicalCount)
}

type stockAdjustmentWireInput struct {
	ProductID int64  `json:"product_id"`
	Quantity  *int64 `json:"quantity"`
}

func stockAdjustmentWireInputDigest(s stockReceiptScope, op stockAdjustmentOperation, input stockAdjustmentWireInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope stockReceiptScope
		Input stockAdjustmentWireInput
	}{op.action(), s, input})
	return sha256.Sum256(data)
}

func (a *app) submitStockAdjustmentProposal(w http.ResponseWriter, r *http.Request, op stockAdjustmentOperation) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptStockAdjustmentProposal(w, r, op); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptStockAdjustmentProposal(w http.ResponseWriter, r *http.Request, op stockAdjustmentOperation) error {
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
	if len(raw) > 512 {
		return badInput
	}
	var body struct {
		RequestID  string          `json:"request_id"`
		Adjustment json.RawMessage `json:"adjustment"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "adjustment"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	var in stockAdjustmentWireInput
	if err = decodeWikiProposalJSON(body.Adjustment, &in, "product_id", "quantity"); err != nil || in.Quantity == nil || !op.valid(in.ProductID, *in.Quantity) {
		return badInput
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadNamedProposalSession(ctx, tx, hash, &scope, op.action())
	if err != nil {
		return err
	}
	s := stockReceiptScopeFrom(scope)
	if err = a.lockStockReceiptScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := stockAdjustmentWireInputDigest(s, op, in)
	p, err := scanStockAdjustmentProposal(tx.QueryRowContext(ctx, `SELECT `+stockAdjustmentWireColumns+` FROM mcp_stock_adjustment_proposals p
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND p.request_id=$4 AND p.action=$5 FOR UPDATE OF p`,
		scope.actor, scope.entity, scope.outlet, body.RequestID, op.action()))
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
			FROM mcp_stock_adjustment_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		var prepared stockAdjustmentProposal
		if op == stockWaste {
			prepared, err = a.prepareStockWaste(ctx, tx, s, stockWasteInput{in.ProductID, *in.Quantity}, body.RequestID)
		} else {
			prepared, err = a.prepareStockPhysicalCount(ctx, tx, s, stockPhysicalCountInput{in.ProductID, in.Quantity}, body.RequestID)
		}
		if err != nil {
			return err
		}
		adjustmentDigest := prepared.digest()
		preview, err := json.Marshal(prepared.Preview)
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		p, err = scanStockAdjustmentProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_stock_adjustment_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,action,request_id,input_digest,adjustment_digest,
			adjustment_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10,$11) RETURNING `+stockAdjustmentWireColumns,
			session, scope.actor, scope.entity, scope.outlet, op.action(), body.RequestID, inputDigest[:], adjustmentDigest[:], preview, now.Unix(), expiry))
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
	jsonResponse(w, status, p.stockAdjustmentWireReceipt)
	return nil
}

func (a *app) listStockAdjustmentProposals(w http.ResponseWriter, r *http.Request, u user, op stockAdjustmentOperation) error {
	tx, s, login, err := a.beginStockAdjustmentReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.movement_id
		FROM mcp_stock_adjustment_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6 AND p.action=$7
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix(), op.action())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []stockAdjustmentWireReceipt{}
	for rows.Next() {
		var item stockAdjustmentWireReceipt
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

func lookupStockAdjustmentProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte, op stockAdjustmentOperation) (stockAdjustmentWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return stockAdjustmentWireRow{}, wikiProposalMissing
	}
	return scanStockAdjustmentProposal(tx.QueryRowContext(r.Context(), `SELECT `+stockAdjustmentWireColumns+` FROM mcp_stock_adjustment_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5 AND p.action=$6
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:], op.action()))
}

func (p stockAdjustmentWireRow) validatedStockAdjustment(s wikiProposalScope, op stockAdjustmentOperation) (stockAdjustmentProposal, error) {
	var preview stockAdjustmentPreview
	if len(p.adjustmentDigest) != 32 || json.Unmarshal(p.preview, &preview) != nil {
		return stockAdjustmentProposal{}, stockAdjustmentStale
	}
	v := stockAdjustmentProposal{op, stockReceiptScopeFrom(s), p.requestID, preview}
	actual := v.digest()
	if subtle.ConstantTimeCompare(actual[:], p.adjustmentDigest) != 1 {
		return v, stockAdjustmentStale
	}
	return v, nil
}

func (a *app) viewStockAdjustmentProposal(w http.ResponseWriter, r *http.Request, u user, op stockAdjustmentOperation) error {
	tx, s, login, err := a.beginStockAdjustmentReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupStockAdjustmentProposal(r, tx, s, login, op)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.stockAdjustmentProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := stockAdjustmentWireDetails{stockAdjustmentWireReceipt: p.stockAdjustmentWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedStockAdjustment(s, op)
		if err != nil {
			return err
		}
		result.Adjustment = &draft.Preview
		result.Digest = hex.EncodeToString(p.adjustmentDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setStockAdjustmentProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, movementID *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_stock_adjustment_proposals SET status=$1,adjustment_preview=NULL,
		decided_at=$2,movement_id=$3 WHERE id=$4 AND status='pending'`, status, now, movementID, id))
}

func (a *app) decideStockAdjustmentProposal(w http.ResponseWriter, r *http.Request, u user, op stockAdjustmentOperation) error {
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
	tx, s, login, err := a.beginStockAdjustmentReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupStockAdjustmentProposal(r, tx, s, login, op)
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
	state, err := a.stockAdjustmentProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setStockAdjustmentProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.adjustmentDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedStockAdjustment(s, op)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		movement, err := a.applyStockAdjustment(r, tx, u, stockReceiptScopeFrom(s), draft, confirmed, a.tokenTime())
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
	if err = setStockAdjustmentProposalTerminal(ctx, tx, p.ID, p.Status, now, p.MovementID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.stockAdjustmentWireReceipt)
	return nil
}

func (a *app) beginStockAdjustmentReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
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
		err = a.lockStockReceiptScope(r.Context(), tx, stockReceiptScopeFrom(s))
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

func (a *app) stockAdjustmentProposalState(ctx context.Context, tx *sql.Tx, p stockAdjustmentWireRow) (string, error) {
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

func (a *app) pruneStockAdjustmentProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_stock_adjustment_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_stock_adjustment_proposals p SET status=ended.state,adjustment_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}
