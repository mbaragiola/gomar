package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

const stockReceiptPurpose = "stock_receipt.v1"

var stockReceiptStale = problem{409, "Cambió el producto o su configuración de stock. Prepará otra recepción."}
var stockReceiptUnavailable = problem{503, "Las recepciones de stock no están habilitadas."}

type stockReceiptScope struct {
	Actor  int64
	Entity int64
	Outlet int64
}

type stockReceiptInput struct {
	ProductID int64 `json:"product_id"`
	Quantity  int64 `json:"quantity"`
}

type stockReceiptPreview struct {
	stockReceiptInput
	ProductName     string `json:"product_name"`
	ProductRevision int64  `json:"product_revision"`
	StockMinimum    int64  `json:"stock_minimum"`
}

// The approval store keeps the normalized request ID. The model cannot
// choose a stock movement key or movement kind.
type stockReceiptProposal struct {
	Scope     stockReceiptScope
	RequestID string
	Preview   stockReceiptPreview
}

func decodeStockReceiptProposal(raw []byte) (stockReceiptInput, error) {
	var input stockReceiptInput
	if len(raw) > 512 {
		return input, badInput
	}
	if err := decodePayables(raw, &input, "product_id", "quantity"); err != nil {
		return input, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return input, badInput
	}
	seen := map[string]bool{}
	for d.More() {
		next, err := d.Token()
		key, ok := next.(string)
		if err != nil || !ok || seen[key] {
			return input, badInput
		}
		seen[key] = true
		var field json.RawMessage
		if err = d.Decode(&field); err != nil {
			return input, badInput
		}
	}
	return input, validateStockReceiptInput(input)
}

func validateStockReceiptInput(input stockReceiptInput) error {
	if input.ProductID < 1 || input.Quantity < 1 || input.Quantity > stockQuantityMax {
		return badInput
	}
	return nil
}

func (p stockReceiptProposal) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose   string
		Scope     stockReceiptScope
		RequestID string
		Preview   stockReceiptPreview
	}{stockReceiptPurpose, p.Scope, p.RequestID, p.Preview})
	return sha256.Sum256(raw)
}

// Derive a stable domain-separated UUID-shaped key for the immutable native
// movement. This stays constant across retries and is never accepted as input.
func stockReceiptMovementID(s stockReceiptScope, requestID string) string {
	raw, _ := json.Marshal(struct {
		Purpose   string
		Scope     stockReceiptScope
		RequestID string
	}{
		stockReceiptPurpose + ".movement", s, requestID})
	hash := sha256.Sum256(raw)
	hash[6] = hash[6]&0x0f | 0x40
	hash[8] = hash[8]&0x3f | 0x80
	key := hex.EncodeToString(hash[:16])
	return key[:8] + "-" + key[8:12] + "-" + key[12:16] + "-" + key[16:20] + "-" + key[20:]
}

func (a *app) lockStockReceiptScope(ctx context.Context, tx *sql.Tx, s stockReceiptScope) error {
	if !a.enablePOS || !a.enableStock {
		return stockReceiptUnavailable
	}
	if s.Actor <= 0 || s.Entity <= 0 || s.Outlet <= 0 {
		return badInput
	}
	// Entity management locks the entity before changing membership or
	// outlets. Take our entity share first to match that order.
	var entity int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM entities WHERE id=$1 AND active FOR SHARE`, s.Entity).Scan(&entity)
	if errors.Is(err, sql.ErrNoRows) {
		return missingEntity
	}
	if err != nil {
		return err
	}
	var membership int64
	var role string
	err = tx.QueryRowContext(ctx, `SELECT m.id,m.role
		FROM memberships m JOIN users u ON u.id=m.user_id AND u.active
		WHERE m.entity_id=$1 AND m.user_id=$2 AND m.active FOR SHARE OF m`, s.Entity, s.Actor).
		Scan(&membership, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return missingEntity
	}
	if err != nil {
		return err
	}
	// Same operational roles as the native POS+stock movement route.
	switch role {
	case "administration", "purchasing", "accounting":
	default:
		return forbiddenRole
	}
	var o int64
	// Use the native outlet's write lock before taking any weaker outlet
	// lock (as taken by a generic scope helper). This serializes native
	// sales, catalog writes, stock settings and stock movements.
	err = tx.QueryRowContext(ctx, `SELECT o.id FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id
		WHERE o.id=$1 AND o.entity_id=$2 AND o.active AND g.membership_id=$3 FOR UPDATE OF o FOR SHARE OF g`,
		s.Outlet, s.Entity, membership).Scan(&o)
	if errors.Is(err, sql.ErrNoRows) {
		return missingOutlet
	}
	return err
}

func readStockReceiptPreview(ctx context.Context, tx *sql.Tx, s stockReceiptScope, input stockReceiptInput) (stockReceiptPreview, error) {
	p := stockReceiptPreview{stockReceiptInput: input}
	err := tx.QueryRowContext(ctx, `SELECT v.name,p.current_revision,t.minimum
		FROM pos_products p JOIN pos_product_versions v ON v.outlet_id=p.outlet_id AND v.product_id=p.id AND v.revision=p.current_revision
		JOIN stock_products t ON t.outlet_id=p.outlet_id AND t.product_id=p.id
		JOIN stock_outlets so ON so.outlet_id=p.outlet_id
		WHERE p.outlet_id=$1 AND p.id=$2 AND v.active AND t.enabled AND so.enabled`,
		s.Outlet, input.ProductID).Scan(&p.ProductName, &p.ProductRevision, &p.StockMinimum)
	if errors.Is(err, sql.ErrNoRows) {
		return p, stockNotTracked
	}
	return p, err
}

// The caller authenticates the purpose-scoped proposal request. Preparation
// locks the outlet for a coherent snapshot but writes no inventory rows.
func (a *app) prepareStockReceiptProposal(ctx context.Context, tx *sql.Tx, s stockReceiptScope, input stockReceiptInput, requestID string) (stockReceiptProposal, error) {
	var empty stockReceiptProposal
	if !posRequestID.MatchString(requestID) {
		return empty, badInput
	}
	if err := validateStockReceiptInput(input); err != nil {
		return empty, err
	}
	if err := a.lockStockReceiptScope(ctx, tx, s); err != nil {
		return empty, err
	}
	p, err := readStockReceiptPreview(ctx, tx, s, input)
	if err != nil {
		return empty, err
	}
	return stockReceiptProposal{s, requestID, p}, nil
}

// The caller has checked explicit same-actor browser approval, proposal
// purpose, lifetime and revocation. It must write the terminal receipt in
// the same transaction. Only the native receipt movement can be committed.
func (a *app) applyStockReceiptProposal(r *http.Request, tx *sql.Tx, u user, s stockReceiptScope, p stockReceiptProposal, confirmed [32]byte, now time.Time) (stockMovement, error) {
	var empty stockMovement
	if u.id == 0 || u.id != s.Actor {
		return empty, unauthenticated
	}
	digest := p.digest()
	if p.Scope != s || subtle.ConstantTimeCompare(digest[:], confirmed[:]) != 1 {
		return empty, stockReceiptStale
	}
	if !posRequestID.MatchString(p.RequestID) || validateStockReceiptInput(p.Preview.stockReceiptInput) != nil {
		return empty, stockReceiptStale
	}
	if err := a.lockStockReceiptScope(r.Context(), tx, s); err != nil {
		return empty, err
	}
	active, err := currentUser(nil, r, tx)
	if err != nil {
		return empty, err
	}
	if active.id != u.id {
		return empty, unauthenticated
	}
	fresh, err := readStockReceiptPreview(r.Context(), tx, s, p.Preview.stockReceiptInput)
	if err == stockNotTracked {
		return empty, stockReceiptStale
	}
	if err != nil {
		return empty, err
	}
	if fresh != p.Preview {
		return empty, stockReceiptStale
	}
	quantity := fresh.Quantity
	_, row, err := recordStockMovement(r.Context(), tx, s.Outlet, fresh.ProductID, s.Actor, stockInput{
		RequestID: stockReceiptMovementID(s, p.RequestID), Kind: "recepcion", Quantity: &quantity,
	}, now)
	if err != nil {
		return empty, err
	}
	return row.(stockMovement), nil
}
