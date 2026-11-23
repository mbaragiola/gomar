package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const posPriceSessionAction = "pos_product_price_change"
const posAvailabilitySessionAction = "pos_product_availability_change"
const posBarcodeSessionAction = "pos_product_barcode_change"

type posCatalogOperation uint8

const (
	posCatalogPrice posCatalogOperation = iota + 1
	posCatalogAvailability
	posCatalogBarcode
)

func (op posCatalogOperation) action() string {
	if op == posCatalogPrice {
		return posPriceSessionAction
	}
	if op == posCatalogAvailability {
		return posAvailabilitySessionAction
	}
	if op == posCatalogBarcode {
		return posBarcodeSessionAction
	}
	return ""
}

func (a *app) posCatalogProposalsEnabled() bool {
	return a.enableMCPPOSCatalogProposals && a.enablePOS
}

type posCatalogWireReceipt struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"`
	Expires         string `json:"expires_at"`
	ProductID       *int64 `json:"product_id,omitempty"`
	ProductRevision *int64 `json:"product_revision,omitempty"`
}
type posCatalogWireDetails struct {
	posCatalogWireReceipt
	Change *posCatalogPreview `json:"change,omitempty"`
	Digest string             `json:"digest,omitempty"`
}
type posCatalogWireRow struct {
	posCatalogWireReceipt
	sessionID, expiry                  int64
	inputDigest, changeDigest, preview []byte
}

const posCatalogWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.product_id,p.product_revision,p.input_digest,p.change_digest,p.change_preview`

func scanPOSCatalogProposal(row interface{ Scan(...any) error }) (posCatalogWireRow, error) {
	var p posCatalogWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.ProductID, &p.ProductRevision, &p.inputDigest, &p.changeDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPPOSCatalogProposals(add func(string, string, http.HandlerFunc)) {
	if !a.posCatalogProposalsEnabled() {
		return
	}
	bind := func(slug string, op posCatalogOperation) {
		browser := "/api/v1/entities/{entity}/outlets/{outlet}/" + slug
		api := "/api/v1/mcp/entities/{entity}/outlets/{outlet}/" + slug
		add(browser, http.MethodGet, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
			return a.listPOSCatalogProposals(w, r, u, op)
		}))
		add(browser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
			return a.viewPOSCatalogProposal(w, r, u, op)
		}))
		add(browser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(func(w http.ResponseWriter, r *http.Request, u user) error {
			return a.decidePOSCatalogProposal(w, r, u, op)
		}))
		add(api, http.MethodPost, func(w http.ResponseWriter, r *http.Request) { a.submitPOSCatalogProposal(w, r, op) })
	}
	bind("pos-price-proposals", posCatalogPrice)
	bind("pos-availability-proposals", posCatalogAvailability)
	bind("pos-barcode-proposals", posCatalogBarcode)
}

type posCatalogPreview struct {
	Product        int64   `json:"product_id"`
	Revision       int64   `json:"expected_revision"`
	Name           string  `json:"name"`
	Price          int64   `json:"price_minor"`
	CurrentPrice   int64   `json:"current_price_minor"`
	Active         bool    `json:"active"`
	CurrentActive  bool    `json:"current_active"`
	Barcode        *string `json:"barcode,omitempty"`
	CurrentBarcode *string `json:"current_barcode,omitempty"`
}

func (p posCatalogPreview) priceProposal(s posRenameScope) posPriceProposal {
	return posPriceProposal{s, posPriceInput{p.Product, p.Revision, p.Price}, p.Name, p.CurrentPrice, p.CurrentActive}
}

func (p posCatalogPreview) availabilityProposal(s posRenameScope) posAvailabilityProposal {
	return posAvailabilityProposal{s, posAvailabilityInput{p.Product, p.Revision, p.Active}, p.Name, p.CurrentPrice, p.CurrentActive}
}

func (p posCatalogPreview) digest(op posCatalogOperation, s posRenameScope) [32]byte {
	return posCatalogDigest(op.action(), struct {
		Scope   posRenameScope
		Preview posCatalogPreview
	}{s, p})
}

func (a *app) submitPOSCatalogProposal(w http.ResponseWriter, r *http.Request, op posCatalogOperation) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptPOSCatalogProposal(w, r, op); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptPOSCatalogProposal(w http.ResponseWriter, r *http.Request, op posCatalogOperation) error {
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
		RequestID string          `json:"request_id"`
		Change    json.RawMessage `json:"change"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "change"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	var price posPriceInput
	var availability posAvailabilityInput
	var barcode *posBarcodeInput
	switch op {
	case posCatalogPrice:
		price, err = decodePOSPrice(body.Change)
	case posCatalogAvailability:
		availability, err = decodePOSAvailability(body.Change)
	case posCatalogBarcode:
		var input posBarcodeInput
		input, err = decodePOSBarcode(body.Change)
		barcode = &input
	default:
		return badInput
	}
	if err != nil {
		return err
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
	s := posRenameScopeFrom(scope)
	if _, err = a.lockPOSRenameScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := posCatalogDigest(op.action(), struct {
		Scope        posRenameScope
		Price        posPriceInput
		Availability posAvailabilityInput
		Barcode      *posBarcodeInput `json:",omitempty"`
	}{s, price, availability, barcode})
	p, err := scanPOSCatalogProposal(tx.QueryRowContext(ctx, `SELECT `+posCatalogWireColumns+` FROM mcp_pos_catalog_proposals p
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
			FROM mcp_pos_catalog_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		var prepared posCatalogPreview
		switch op {
		case posCatalogPrice:
			p, err := readPOSPricePreview(ctx, tx, s, price)
			if err != nil {
				return err
			}
			prepared = posCatalogPreview{Product: price.Product, Revision: price.Revision, Name: p.Name,
				Price: price.Price, CurrentPrice: p.PreviousPrice, Active: p.Active, CurrentActive: p.Active}
		case posCatalogAvailability:
			p, err := readPOSAvailabilityPreview(ctx, tx, s, availability)
			if err != nil {
				return err
			}
			prepared = posCatalogPreview{Product: availability.Product, Revision: availability.Revision, Name: p.Name,
				Price: p.Price, CurrentPrice: p.Price, Active: availability.Active, CurrentActive: p.PreviousActive}
		case posCatalogBarcode:
			p, err := readPOSBarcodeChange(ctx, tx, s.Outlet, *barcode)
			if err != nil {
				return err
			}
			prepared = posCatalogPreview{Product: p.ID, Revision: p.Revision, Name: p.Name, Price: p.Price,
				CurrentPrice: p.Price, Active: p.Active, CurrentActive: p.Active, Barcode: &barcode.Barcode, CurrentBarcode: &p.Barcode}
		}
		changeDigest := prepared.digest(op, s)
		preview, err := json.Marshal(prepared)
		if err != nil {
			return err
		}

		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		p, err = scanPOSCatalogProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_pos_catalog_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,action,request_id,input_digest,change_digest,
			change_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10,$11) RETURNING `+posCatalogWireColumns,
			session, scope.actor, scope.entity, scope.outlet, op.action(), body.RequestID, inputDigest[:], changeDigest[:], preview, now.Unix(), expiry))
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
	jsonResponse(w, status, p.posCatalogWireReceipt)
	return nil
}

func (a *app) listPOSCatalogProposals(w http.ResponseWriter, r *http.Request, u user, op posCatalogOperation) error {
	tx, s, login, err := a.beginPOSCatalogReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.product_id,p.product_revision
		FROM mcp_pos_catalog_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6 AND p.action=$7
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix(), op.action())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []posCatalogWireReceipt{}
	for rows.Next() {
		var item posCatalogWireReceipt
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

func lookupPOSCatalogProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte, op posCatalogOperation) (posCatalogWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return posCatalogWireRow{}, wikiProposalMissing
	}
	return scanPOSCatalogProposal(tx.QueryRowContext(r.Context(), `SELECT `+posCatalogWireColumns+` FROM mcp_pos_catalog_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5 AND p.action=$6
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:], op.action()))
}

func (p posCatalogWireRow) validatedPOSCatalog(s wikiProposalScope, op posCatalogOperation) (posCatalogPreview, error) {
	var preview posCatalogPreview
	if len(p.changeDigest) != 32 || json.Unmarshal(p.preview, &preview) != nil {
		return preview, posChangedCatalog
	}
	actual := preview.digest(op, posRenameScopeFrom(s))
	if subtle.ConstantTimeCompare(actual[:], p.changeDigest) != 1 {
		return preview, posChangedCatalog
	}
	return preview, nil
}

func (a *app) viewPOSCatalogProposal(w http.ResponseWriter, r *http.Request, u user, op posCatalogOperation) error {
	tx, s, login, err := a.beginPOSCatalogReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupPOSCatalogProposal(r, tx, s, login, op)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.posCatalogProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := posCatalogWireDetails{posCatalogWireReceipt: p.posCatalogWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedPOSCatalog(s, op)
		if err != nil {
			return err
		}
		result.Change = &draft
		result.Digest = hex.EncodeToString(p.changeDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setPOSCatalogProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, productID, revision *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_pos_catalog_proposals SET status=$1,change_preview=NULL,
		decided_at=$2,product_id=$3,product_revision=$4 WHERE id=$5 AND status='pending'`, status, now, productID, revision, id))
}

func (a *app) decidePOSCatalogProposal(w http.ResponseWriter, r *http.Request, u user, op posCatalogOperation) error {
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
	tx, s, login, err := a.beginPOSCatalogReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupPOSCatalogProposal(r, tx, s, login, op)
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
	state, err := a.posCatalogProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setPOSCatalogProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil, nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.changeDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedPOSCatalog(s, op)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		scope := posRenameScopeFrom(s)
		var product posProduct
		switch op {
		case posCatalogPrice:
			proposal := draft.priceProposal(scope)
			product, err = a.applyPOSPrice(r, tx, u, scope, proposal, proposal.digest())
		case posCatalogAvailability:
			proposal := draft.availabilityProposal(scope)
			product, err = a.applyPOSAvailability(r, tx, u, scope, proposal, proposal.digest())
		case posCatalogBarcode:
			if draft.Barcode == nil || draft.CurrentBarcode == nil {
				return posChangedCatalog
			}
			current, changeErr := readPOSBarcodeChange(ctx, tx, s.outlet, posBarcodeInput{draft.Product, draft.Revision, *draft.Barcode})
			if changeErr != nil {
				return changeErr
			}
			if current.Barcode != *draft.CurrentBarcode || current.Name != draft.Name ||
				current.Price != draft.CurrentPrice || draft.Price != draft.CurrentPrice ||
				current.Active != draft.CurrentActive || draft.Active != draft.CurrentActive {
				return posChangedCatalog
			}
			product, err = applyPOSBarcodeChange(ctx, tx, s.outlet, current, *draft.Barcode)
		default:
			return badInput
		}
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
	if err = setPOSCatalogProposalTerminal(ctx, tx, p.ID, p.Status, now, p.ProductID, p.ProductRevision); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.posCatalogWireReceipt)
	return nil
}

func (a *app) beginPOSCatalogReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
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

func (a *app) posCatalogProposalState(ctx context.Context, tx *sql.Tx, p posCatalogWireRow) (string, error) {
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

func (a *app) prunePOSCatalogProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_pos_catalog_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_pos_catalog_proposals p SET status=ended.state,change_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}
