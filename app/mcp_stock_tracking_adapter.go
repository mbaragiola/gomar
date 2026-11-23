package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

type stockTrackingInput struct {
	Product         int64 `json:"product_id"`
	Revision        int64 `json:"expected_revision"`
	TrackingEnabled *bool `json:"tracking_enabled"`
}

type stockTrackingPreview struct {
	Scope           posRenameScope `json:"-"`
	Product         int64          `json:"product_id"`
	Revision        int64          `json:"expected_revision"`
	Name            string         `json:"name"`
	Previous        bool           `json:"current_tracking_enabled"`
	TrackingEnabled bool           `json:"tracking_enabled"`
	Minimum         int64          `json:"minimum"`
}

var stockTrackingNoChange = problem{422, "El seguimiento de stock no cambió."}
var stockTrackingChanged = problem{409, "Cambió el producto o su configuración de stock. Prepará otra propuesta."}
var stockTrackingAcknowledgmentRequired = problem{422, "Confirmá que no se descontará stock por las ventas registradas mientras esté desactivado y que no se recuperarán esos descuentos."}

func decodeStockTracking(raw []byte) (stockTrackingInput, error) {
	var in stockTrackingInput
	if len(raw) > 512 || decodeWikiProposalJSON(raw, &in, "product_id", "expected_revision", "tracking_enabled") != nil ||
		!validPOSCatalogTarget(in.Product, in.Revision) || in.TrackingEnabled == nil {
		return stockTrackingInput{}, badInput
	}
	return in, nil
}

func (p stockTrackingPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   posRenameScope
		Preview stockTrackingPreview
	}{"stock_tracking.v1", p.Scope, p})
	return sha256.Sum256(raw)
}

func (a *app) lockStockTrackingScope(ctx context.Context, tx *sql.Tx, s posRenameScope) error {
	if !a.stockTrackingProposalsEnabled() {
		return stockReceiptUnavailable
	}
	_, err := a.lockPOSRenameScope(ctx, tx, s)
	return err
}

// Caller holds the same outlet lock used by native catalog, stock and sales
// writers. Only existing stock settings in a stock-enabled outlet qualify.
func readStockTracking(ctx context.Context, tx *sql.Tx, s posRenameScope, in stockTrackingInput) (stockTrackingPreview, error) {
	if !validPOSCatalogTarget(in.Product, in.Revision) || in.TrackingEnabled == nil {
		return stockTrackingPreview{}, badInput
	}
	p := stockTrackingPreview{Scope: s, Product: in.Product, TrackingEnabled: *in.TrackingEnabled}
	err := tx.QueryRowContext(ctx, `SELECT p.current_revision,v.name,t.enabled,t.minimum
  FROM pos_products p
  JOIN pos_product_versions v ON (v.product_id,v.outlet_id,v.revision)=(p.id,p.outlet_id,p.current_revision)
  JOIN stock_products t ON (t.product_id,t.outlet_id)=(p.id,p.outlet_id)
  JOIN stock_outlets o ON o.outlet_id=p.outlet_id
  WHERE p.id=$1 AND p.outlet_id=$2 AND v.active AND o.enabled`, in.Product, s.Outlet).
		Scan(&p.Revision, &p.Name, &p.Previous, &p.Minimum)
	if errors.Is(err, sql.ErrNoRows) {
		return stockTrackingPreview{}, posMissingProduct
	}
	if err != nil {
		return stockTrackingPreview{}, err
	}
	if p.Revision != in.Revision {
		return stockTrackingPreview{}, stockTrackingChanged
	}
	if p.Previous == p.TrackingEnabled {
		return stockTrackingPreview{}, stockTrackingNoChange
	}
	return p, nil
}

func (a *app) applyStockTracking(r *http.Request, tx *sql.Tx, u user, s posRenameScope, p stockTrackingPreview, approved [32]byte) error {
	if u.id <= 0 || u.id != s.Actor {
		return unauthenticated
	}
	d := p.digest()
	if s != p.Scope || subtle.ConstantTimeCompare(d[:], approved[:]) != 1 {
		return stockTrackingChanged
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	ctx := r.Context()
	if err := a.lockStockTrackingScope(ctx, tx, s); err != nil {
		return err
	}
	fresh, err := readStockTracking(ctx, tx, s, stockTrackingInput{p.Product, p.Revision, &p.TrackingEnabled})
	if err != nil && err != posMissingProduct && err != stockTrackingNoChange && err != stockTrackingChanged {
		return err
	}
	if err != nil || fresh != p {
		return stockTrackingChanged
	}
	result, err := tx.ExecContext(ctx, `UPDATE stock_products SET enabled=$1 WHERE outlet_id=$2 AND product_id=$3 AND enabled=$4 AND minimum=$5
 AND EXISTS(SELECT 1 FROM stock_outlets o WHERE o.outlet_id=$2 AND o.enabled)
 AND EXISTS(SELECT 1 FROM pos_products p JOIN pos_product_versions v
 ON (v.product_id,v.outlet_id,v.revision)=(p.id,p.outlet_id,p.current_revision)
 WHERE p.id=$3 AND p.outlet_id=$2 AND p.current_revision=$6 AND v.active)`,
		p.TrackingEnabled, s.Outlet, p.Product, p.Previous, p.Minimum, p.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return stockTrackingChanged
	}
	return nil
}
