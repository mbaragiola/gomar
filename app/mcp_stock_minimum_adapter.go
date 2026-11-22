package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
)

type stockMinimumInput struct {
	Product  int64  `json:"product_id"`
	Revision int64  `json:"expected_revision"`
	Minimum  *int64 `json:"minimum"`
}

type stockMinimumPreview struct {
	Scope    posRenameScope `json:"-"`
	Product  int64          `json:"product_id"`
	Revision int64          `json:"expected_revision"`
	Name     string         `json:"name"`
	Previous int64          `json:"current_minimum"`
	Minimum  int64          `json:"minimum"`
}

var stockMinimumNoChange = problem{422, "El stock mínimo no cambió."}

var stockMinimumChanged = problem{409, "Cambió el producto o el stock mínimo. Prepará otra propuesta."}

func decodeStockMinimum(raw []byte) (stockMinimumInput, error) {
	var in stockMinimumInput
	if len(raw) > 512 {
		return in, badInput
	}
	if err := decodeWikiProposalJSON(raw, &in, "product_id", "expected_revision", "minimum"); err != nil {
		return in, err
	}
	return in, validateStockMinimum(in)
}

func validateStockMinimum(in stockMinimumInput) error {
	if in.Product <= 0 || in.Revision <= 0 || in.Minimum == nil || *in.Minimum < 0 || *in.Minimum > stockQuantityMax {
		return badInput
	}
	return nil
}

func (p stockMinimumPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   posRenameScope
		Preview stockMinimumPreview
	}{"stock_minimum.v1", p.Scope, p})
	return sha256.Sum256(raw)
}

func (a *app) lockStockMinimumScope(ctx context.Context, tx *sql.Tx, s posRenameScope) error {
	if !a.enableStock || !a.enablePOS {
		return stockReceiptUnavailable
	}
	_, err := a.lockPOSRenameScope(ctx, tx, s)
	return err
}

// The native outlet write lock protects the read from concurrent updates to
// stock settings and the catalog. No inventory data is changed here.
func (a *app) prepareStockMinimum(ctx context.Context, tx *sql.Tx, s posRenameScope, in stockMinimumInput) (stockMinimumPreview, error) {
	if err := validateStockMinimum(in); err != nil {
		return stockMinimumPreview{}, err
	}
	if err := a.lockStockMinimumScope(ctx, tx, s); err != nil {
		return stockMinimumPreview{}, err
	}
	return readStockMinimum(ctx, tx, s, in)
}

func readStockMinimum(ctx context.Context, tx *sql.Tx, s posRenameScope, in stockMinimumInput) (stockMinimumPreview, error) {
	// Only already tracked, active products in an enabled outlet qualify.
	old, err := readStockReceiptPreview(ctx, tx, stockReceiptScope{Actor: s.Actor, Entity: s.Entity, Outlet: s.Outlet}, stockReceiptInput{ProductID: in.Product})
	if err != nil {
		return stockMinimumPreview{}, err
	}
	if old.ProductRevision != in.Revision {
		return stockMinimumPreview{}, stockMinimumChanged
	}
	if old.StockMinimum == *in.Minimum {
		return stockMinimumPreview{}, stockMinimumNoChange
	}
	return stockMinimumPreview{s, in.Product, in.Revision, old.ProductName, old.StockMinimum, *in.Minimum}, nil
}

// The future browser handler locks the exact issuing login and pending
// proposal and consumes the receipt in this transaction.
func (a *app) applyStockMinimum(r *http.Request, tx *sql.Tx, u user, s posRenameScope, p stockMinimumPreview, approved [32]byte) error {
	if u.id <= 0 || u.id != s.Actor {
		return unauthenticated
	}
	d := p.digest()
	if s != p.Scope || subtle.ConstantTimeCompare(d[:], approved[:]) != 1 {
		return stockMinimumChanged
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	ctx := r.Context()
	if err := a.lockStockMinimumScope(ctx, tx, s); err != nil {
		return err
	}
	in := stockMinimumInput{p.Product, p.Revision, &p.Minimum}
	if err := validateStockMinimum(in); err != nil {
		return err
	}
	fresh, err := readStockMinimum(ctx, tx, s, in)
	if err != nil || fresh != p {
		if err == nil || err == stockNotTracked || err == stockMinimumChanged || err == stockMinimumNoChange {
			return stockMinimumChanged
		}
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE stock_products SET minimum=$1 WHERE outlet_id=$2 AND product_id=$3 AND enabled AND minimum=$4
 AND EXISTS (SELECT 1 FROM stock_outlets o WHERE o.outlet_id=$2 AND o.enabled)
 AND EXISTS (SELECT 1 FROM pos_products p JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
 WHERE p.id=$3 AND p.outlet_id=$2 AND p.current_revision=$5 AND v.active)`,
		p.Minimum, s.Outlet, p.Product, p.Previous, p.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return stockMinimumChanged
	}
	return nil
}
