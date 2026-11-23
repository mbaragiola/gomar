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

type stockProductSetupInput struct {
	Product  int64  `json:"product_id"`
	Revision int64  `json:"expected_revision"`
	Minimum  *int64 `json:"minimum"`
}

type stockProductSetupPreview struct {
	Scope           posRenameScope `json:"-"`
	Product         int64          `json:"product_id"`
	Revision        int64          `json:"expected_revision"`
	Name            string         `json:"name"`
	Minimum         int64          `json:"minimum"`
	TrackingEnabled bool           `json:"tracking_enabled"`
}

var stockProductSetupChanged = problem{409, "Cambió el producto o la configuración de stock. Prepará otra propuesta."}
var stockProductSetupExists = problem{409, "Este producto ya tiene configuración de stock. No se modificó."}

func validateStockProductSetup(in stockProductSetupInput) error {
	if !validPOSCatalogTarget(in.Product, in.Revision) || in.Minimum == nil || *in.Minimum < 0 || *in.Minimum > stockQuantityMax {
		return badInput
	}
	return nil
}

func decodeStockProductSetup(raw []byte) (stockProductSetupInput, error) {
	var in stockProductSetupInput
	if len(raw) > 512 || decodeWikiProposalJSON(raw, &in, "product_id", "expected_revision", "minimum") != nil {
		return stockProductSetupInput{}, badInput
	}
	return in, validateStockProductSetup(in)
}

func (p stockProductSetupPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   posRenameScope
		Preview stockProductSetupPreview
	}{"stock_product_setup.v1", p.Scope, p})
	return sha256.Sum256(raw)
}

func (a *app) lockStockProductSetupScope(ctx context.Context, tx *sql.Tx, s posRenameScope) error {
	if !a.stockProductSetupProposalsEnabled() {
		return wikiProposalUnavailable
	}
	_, err := a.lockPOSRenameScope(ctx, tx, s)
	return err
}

// Caller holds the native outlet lock, including when applying the approval.
// A missing stock row is the only eligible state; the movement FK also means
// that there cannot be existing stock history without a stock row.
func readStockProductSetup(ctx context.Context, tx *sql.Tx, s posRenameScope, in stockProductSetupInput) (stockProductSetupPreview, error) {
	if err := validateStockProductSetup(in); err != nil {
		return stockProductSetupPreview{}, err
	}
	p := stockProductSetupPreview{Scope: s, Product: in.Product, Minimum: *in.Minimum}
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT p.current_revision,v.name,
 EXISTS(SELECT 1 FROM stock_products t WHERE t.outlet_id=p.outlet_id AND t.product_id=p.id)
 FROM pos_products p
 JOIN pos_product_versions v ON (v.product_id,v.outlet_id,v.revision)=(p.id,p.outlet_id,p.current_revision)
 JOIN stock_outlets o ON o.outlet_id=p.outlet_id
 WHERE p.id=$1 AND p.outlet_id=$2 AND v.active AND o.enabled`, in.Product, s.Outlet).
		Scan(&p.Revision, &p.Name, &exists)
	if errors.Is(err, sql.ErrNoRows) {
		return stockProductSetupPreview{}, posMissingProduct
	}
	if err != nil {
		return stockProductSetupPreview{}, err
	}
	if p.Revision != in.Revision {
		return stockProductSetupPreview{}, stockProductSetupChanged
	}
	if exists {
		return stockProductSetupPreview{}, stockProductSetupExists
	}
	return p, nil
}

func (a *app) applyStockProductSetup(r *http.Request, tx *sql.Tx, u user, s posRenameScope, p stockProductSetupPreview, approved [32]byte) error {
	if u.id <= 0 || u.id != s.Actor {
		return unauthenticated
	}
	d := p.digest()
	if s != p.Scope || p.TrackingEnabled || subtle.ConstantTimeCompare(d[:], approved[:]) != 1 {
		return stockProductSetupChanged
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	ctx := r.Context()
	if err := a.lockStockProductSetupScope(ctx, tx, s); err != nil {
		return err
	}
	fresh, err := readStockProductSetup(ctx, tx, s, stockProductSetupInput{p.Product, p.Revision, &p.Minimum})
	if err != nil && err != posMissingProduct && err != stockProductSetupExists && err != stockProductSetupChanged {
		return err
	}
	if err != nil || fresh != p {
		return stockProductSetupChanged
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO stock_products(outlet_id,product_id,enabled,minimum)
 SELECT p.outlet_id,p.id,FALSE,$1 FROM pos_products p
 JOIN pos_product_versions v ON (v.product_id,v.outlet_id,v.revision)=(p.id,p.outlet_id,p.current_revision)
 JOIN stock_outlets o ON o.outlet_id=p.outlet_id
 WHERE p.id=$2 AND p.outlet_id=$3 AND p.current_revision=$4 AND v.active AND o.enabled
 AND NOT EXISTS (SELECT 1 FROM stock_products t WHERE t.product_id=p.id AND t.outlet_id=p.outlet_id)`,
		p.Minimum, p.Product, s.Outlet, p.Revision)
	if err != nil {
		if isUnique(err) {
			return stockProductSetupChanged
		}
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return stockProductSetupChanged
	}
	return nil
}
