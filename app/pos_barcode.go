package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/mbaragiola/gomar/app/internal/posbarcode"
)

var posDuplicateBarcode = problem{409, "Ese código de barras ya está asignado a otro producto de este local."}

type posBarcodeInput struct {
	Product  int64  `json:"product_id"`
	Revision int64  `json:"expected_revision"`
	Barcode  string `json:"barcode"`
}

func validPOSBarcode(code string) bool { return code == "" || posbarcode.Valid(code) }

func readPOSBarcodeChange(ctx context.Context, tx *sql.Tx, outlet int64, in posBarcodeInput) (posProduct, error) {
	if !validPOSCatalogTarget(in.Product, in.Revision) || !validPOSBarcode(in.Barcode) {
		return posProduct{}, badInput
	}
	var p posProduct
	err := tx.QueryRowContext(ctx, currentOutletPOSProducts+` AND p.id=$2`, outlet, in.Product).
		Scan(&p.ID, &p.Name, &p.Price, &p.Revision, &p.Active, &p.Barcode)
	if errors.Is(err, sql.ErrNoRows) {
		return p, posMissingProduct
	}
	if err != nil {
		return p, err
	}
	if p.Revision != in.Revision {
		return p, posChangedCatalog
	}
	if p.Barcode == in.Barcode {
		return p, posCatalogNoChange
	}
	if in.Barcode != "" {
		var taken bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM pos_products WHERE outlet_id=$1 AND barcode=$2)`, outlet, in.Barcode).Scan(&taken)
		if err != nil {
			return p, err
		}
		if taken {
			return p, posDuplicateBarcode
		}
	}
	return p, nil
}

// Both direct catalog maintenance and explicit MCP approval call this with
// the same outlet lock. The unique index is still the final duplicate guard.
func applyPOSBarcodeChange(ctx context.Context, tx *sql.Tx, outlet int64, p posProduct, code string) (posProduct, error) {
	err := exactlyOneWrite(tx.ExecContext(ctx, `UPDATE pos_products SET barcode=NULLIF($1,''),current_revision=$2
		WHERE outlet_id=$3 AND id=$4 AND current_revision=$5`, code, p.Revision+1, outlet, p.ID, p.Revision))
	if isUnique(err) {
		return posProduct{}, posDuplicateBarcode
	}
	if err != nil {
		return posProduct{}, err
	}
	p.Revision++
	p.Barcode = code
	_, err = tx.ExecContext(ctx, `INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
		VALUES($1,$2,$3,$4,$5,$6)`, p.ID, outlet, p.Revision, p.Name, p.Price, p.Active)
	return p, err
}

func savePOSBarcode(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	product, err := pathID(r, "product")
	if err != nil {
		return 0, nil, err
	}
	var wire struct {
		Barcode  *string `json:"codigo_barras"`
		Revision int64   `json:"revision"`
	}
	if err := decodeWikiProposalJSON(raw, &wire, "codigo_barras", "revision"); err != nil {
		return 0, nil, err
	}
	if wire.Barcode == nil {
		return 0, nil, badInput
	}
	current, err := readPOSBarcodeChange(r.Context(), tx, outlet, posBarcodeInput{product, wire.Revision, *wire.Barcode})
	if err != nil {
		return 0, nil, err
	}
	result, err := applyPOSBarcodeChange(r.Context(), tx, outlet, current, *wire.Barcode)
	return http.StatusOK, result, err
}

func decodePOSBarcode(raw []byte) (posBarcodeInput, error) {
	var wire struct {
		Product  int64   `json:"product_id"`
		Revision int64   `json:"expected_revision"`
		Barcode  *string `json:"barcode"`
	}
	if len(raw) > 512 {
		return posBarcodeInput{}, badInput
	}
	if err := decodeWikiProposalJSON(raw, &wire, "product_id", "expected_revision", "barcode"); err != nil {
		return posBarcodeInput{}, err
	}
	if wire.Barcode == nil || !validPOSCatalogTarget(wire.Product, wire.Revision) || !validPOSBarcode(*wire.Barcode) {
		return posBarcodeInput{}, badInput
	}
	return posBarcodeInput{wire.Product, wire.Revision, *wire.Barcode}, nil
}
