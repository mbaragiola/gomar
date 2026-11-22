package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const posCreateProposalPurpose = "pos_product_create.v1"

var posCreateCapacity = problem{422, "El catálogo alcanzó el máximo de productos para este local."}
var posCreateCapacityChanged = problem{409, "Cambió el espacio disponible en el catálogo. Prepará otra propuesta."}

type posCreateInput struct {
	Name   string `json:"name"`
	Price  int64  `json:"price_minor"`
	Active bool   `json:"active"`
}

type posCreatePreview struct {
	posCreateInput
	Currency string `json:"currency"`
	Existing int    `json:"catalog_count"`
	Limit    int    `json:"catalog_limit"`
}

type posCreateProposal struct {
	Scope   posRenameScope
	Preview posCreatePreview
}

func decodePOSCreate(raw []byte) (posCreateInput, error) {
	var wire struct {
		Name   string `json:"name"`
		Price  int64  `json:"price_minor"`
		Active *bool  `json:"active"`
	}
	if len(raw) > 1024 || !utf8.Valid(raw) {
		return posCreateInput{}, badInput
	}
	if err := decodeWikiProposalJSON(raw, &wire, "name", "price_minor", "active"); err != nil || wire.Active == nil {
		return posCreateInput{}, badInput
	}
	return normalizePOSCreate(posCreateInput{wire.Name, wire.Price, *wire.Active})
}

func normalizePOSCreate(in posCreateInput) (posCreateInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 120 ||
		strings.ContainsRune(in.Name, 0) || in.Price < 1 || in.Price > posPriceMax {
		return posCreateInput{}, badInput
	}
	return in, nil
}

func (p posCreateProposal) digest() [32]byte {
	return posCatalogDigest(posCreateProposalPurpose, p)
}

// The caller has taken the native outlet lock; revalidation and creation
// must hold the same lock until their transaction ends.
func readPOSCreatePreview(ctx context.Context, tx *sql.Tx, s posRenameScope, in posCreateInput) (posCreateProposal, error) {
	exact, err := normalizePOSCreate(in)
	if err != nil {
		return posCreateProposal{}, err
	}
	p := posCreateProposal{s, posCreatePreview{posCreateInput: exact, Currency: "ARS", Limit: posCatalogMax}}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pos_products WHERE outlet_id=$1`, s.Outlet).Scan(&p.Preview.Existing); err != nil {
		return posCreateProposal{}, err
	}
	if p.Preview.Existing >= p.Preview.Limit {
		return posCreateProposal{}, posCreateCapacity
	}
	return p, nil
}

// Preparation takes the same scope and outlet lock as a native catalog
// writer, but it does not insert a product or reserve capacity.
func (a *app) preparePOSCreate(ctx context.Context, tx *sql.Tx, s posRenameScope, in posCreateInput) (posCreateProposal, error) {
	if _, err := a.lockPOSRenameScope(ctx, tx, s); err != nil {
		return posCreateProposal{}, err
	}
	return readPOSCreatePreview(ctx, tx, s, in)
}

// The future handler must bind one pending proposal and its exact issuing
// browser login, lock the proposal row and persist the terminal actor/
// scope/product receipt in this transaction. An approved replay must read
// that receipt, never call the writer again.
func (a *app) applyPOSCreate(r *http.Request, tx *sql.Tx, u user, s posRenameScope, p posCreateProposal, approved [32]byte) (posProduct, error) {
	var empty posProduct
	m, err := a.lockPOSCatalogApproval(r, tx, u, s, p.Scope, p.digest(), approved)
	if err != nil {
		return empty, err
	}
	fresh, err := readPOSCreatePreview(r.Context(), tx, s, p.Preview.posCreateInput)
	if err == posCreateCapacity {
		return empty, posCreateCapacityChanged
	}
	if err != nil {
		return empty, err
	}
	if fresh != p {
		return empty, posCreateCapacityChanged
	}
	write := r.Clone(r.Context())
	write.Method = http.MethodPost
	write.SetPathValue("outlet", strconv.FormatInt(s.Outlet, 10))
	raw, err := json.Marshal(struct {
		Name   string `json:"nombre"`
		Price  int64  `json:"precio_centavos"`
		Active bool   `json:"activo"`
	}{fresh.Preview.Name, fresh.Preview.Price, fresh.Preview.Active})
	if err != nil {
		return empty, err
	}
	_, saved, err := savePOSProduct(write, tx, m, raw)
	if err != nil {
		return empty, err
	}
	return saved.(posProduct), nil
}
