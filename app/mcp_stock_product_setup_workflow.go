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

const stockProductSetupSessionAction = "stock_product_setup"

func (a *app) stockProductSetupProposalsEnabled() bool {
	return a.enableMCPStockProductSetupProposals && a.enablePOS && a.enableStock
}

const stockProductSetupWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/stock-product-setup-proposals"
const stockProductSetupWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/stock-product-setup-proposals"

type stockProductSetupWireReceipt struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"`
	Expires         string `json:"expires_at"`
	ProductID       *int64 `json:"product_id,omitempty"`
	Minimum         *int64 `json:"minimum,omitempty"`
	TrackingEnabled *bool  `json:"tracking_enabled,omitempty"`
}
type stockProductSetupWireDetails struct {
	stockProductSetupWireReceipt
	StockSetup *stockProductSetupPreview `json:"stock_setup,omitempty"`
	Digest     string                    `json:"digest,omitempty"`
}
type stockProductSetupWireRow struct {
	stockProductSetupWireReceipt
	sessionID, expiry                 int64
	inputDigest, setupDigest, preview []byte
}

const stockProductSetupWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.product_id,p.approved_minimum,CASE WHEN p.status='approved' THEN FALSE END,p.input_digest,p.setup_digest,p.setup_preview`

func scanStockProductSetupProposal(row interface{ Scan(...any) error }) (stockProductSetupWireRow, error) {
	var p stockProductSetupWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.ProductID, &p.Minimum, &p.TrackingEnabled, &p.inputDigest, &p.setupDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPStockProductSetupProposals(add func(string, string, http.HandlerFunc)) {
	if !a.stockProductSetupProposalsEnabled() {
		return
	}
	add(stockProductSetupWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listStockProductSetupProposals))
	add(stockProductSetupWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewStockProductSetupProposal))
	add(stockProductSetupWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideStockProductSetupProposal))
	add(stockProductSetupWireAPI, http.MethodPost, a.submitStockProductSetupProposal)
}

func stockProductSetupWireInputDigest(s posRenameScope, input stockProductSetupInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope posRenameScope
		Input stockProductSetupInput
	}{stockProductSetupSessionAction, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitStockProductSetupProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptStockProductSetupProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptStockProductSetupProposal(w http.ResponseWriter, r *http.Request) error {
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
	if len(raw) > 1280 {
		return badInput
	}
	var body struct {
		RequestID  string          `json:"request_id"`
		StockSetup json.RawMessage `json:"stock_setup"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "stock_setup"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in, err := decodeStockProductSetup(body.StockSetup)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadNamedProposalSession(ctx, tx, hash, &scope, stockProductSetupSessionAction)
	if err != nil {
		return err
	}
	s := posRenameScopeFrom(scope)
	if err = a.lockStockProductSetupScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := stockProductSetupWireInputDigest(s, in)
	p, err := scanStockProductSetupProposal(tx.QueryRowContext(ctx, `SELECT `+stockProductSetupWireColumns+` FROM mcp_stock_product_setup_proposals p
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
			FROM mcp_stock_product_setup_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := readStockProductSetup(ctx, tx, s, in)
		if err != nil {
			return err
		}
		setupDigest := prepared.digest()
		preview, err := json.Marshal(prepared)
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		p, err = scanStockProductSetupProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_stock_product_setup_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,request_id,input_digest,setup_digest,
			setup_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+stockProductSetupWireColumns,
			session, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], setupDigest[:], preview, now.Unix(), expiry))
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
	p.ProductID, p.Minimum, p.TrackingEnabled = nil, nil, nil
	jsonResponse(w, status, p.stockProductSetupWireReceipt)
	return nil
}

func (a *app) listStockProductSetupProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginStockProductSetupReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.product_id,p.approved_minimum,CASE WHEN p.status='approved' THEN FALSE END
		FROM mcp_stock_product_setup_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []stockProductSetupWireReceipt{}
	for rows.Next() {
		var item stockProductSetupWireReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.ProductID, &item.Minimum, &item.TrackingEnabled); err != nil {
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

func lookupStockProductSetupProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte) (stockProductSetupWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return stockProductSetupWireRow{}, wikiProposalMissing
	}
	return scanStockProductSetupProposal(tx.QueryRowContext(r.Context(), `SELECT `+stockProductSetupWireColumns+` FROM mcp_stock_product_setup_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:]))
}

func (p stockProductSetupWireRow) validatedStockProductSetup(s wikiProposalScope) (stockProductSetupPreview, error) {
	var preview stockProductSetupPreview
	if len(p.setupDigest) != 32 || json.Unmarshal(p.preview, &preview) != nil {
		return preview, stockProductSetupChanged
	}
	preview.Scope = posRenameScopeFrom(s)
	got := preview.digest()
	if subtle.ConstantTimeCompare(got[:], p.setupDigest) != 1 {
		return preview, stockProductSetupChanged
	}
	return preview, nil
}

func (a *app) viewStockProductSetupProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginStockProductSetupReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupStockProductSetupProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.stockProductSetupProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := stockProductSetupWireDetails{stockProductSetupWireReceipt: p.stockProductSetupWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedStockProductSetup(s)
		if err != nil {
			return err
		}
		result.StockSetup = &draft
		result.Digest = hex.EncodeToString(p.setupDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setStockProductSetupProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, productID *int64, minimum *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_stock_product_setup_proposals SET status=$1,setup_preview=NULL,
		decided_at=$2,product_id=$3,approved_minimum=$4 WHERE id=$5 AND status='pending'`, status, now, productID, minimum, id))
}

func (a *app) decideStockProductSetupProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	tx, s, login, err := a.beginStockProductSetupReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupStockProductSetupProposal(r, tx, s, login)
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
	state, err := a.stockProductSetupProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setStockProductSetupProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil, nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.setupDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedStockProductSetup(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		if err := a.applyStockProductSetup(r, tx, u, posRenameScopeFrom(s), draft, confirmed); err != nil {
			return err
		}
		p.ProductID, p.Minimum, p.TrackingEnabled = &draft.Product, &draft.Minimum, &draft.TrackingEnabled
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setStockProductSetupProposalTerminal(ctx, tx, p.ID, p.Status, now, p.ProductID, p.Minimum); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.stockProductSetupWireReceipt)
	return nil
}

func (a *app) stockProductSetupProposalState(ctx context.Context, tx *sql.Tx, p stockProductSetupWireRow) (string, error) {
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

func (a *app) pruneStockProductSetupProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_stock_product_setup_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_stock_product_setup_proposals p SET status=ended.state,setup_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}

func (a *app) beginStockProductSetupReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
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
		err = a.lockStockProductSetupScope(r.Context(), tx, posRenameScopeFrom(s))
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
