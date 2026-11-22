package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
)

const posPriceProposalPurpose = "pos_product_price.v1"
const posAvailabilityProposalPurpose = "pos_product_availability.v1"

var posCatalogNoChange = problem{422, "El dato del producto no cambió."}

type posPriceInput struct {
	Product  int64 `json:"product_id"`
	Revision int64 `json:"expected_revision"`
	Price    int64 `json:"price_minor"`
}

type posAvailabilityInput struct {
	Product  int64 `json:"product_id"`
	Revision int64 `json:"expected_revision"`
	Active   bool  `json:"active"`
}

type posPriceProposal struct {
	Scope         posRenameScope
	Input         posPriceInput
	Name          string
	PreviousPrice int64
	Active        bool
}

type posAvailabilityProposal struct {
	Scope          posRenameScope
	Input          posAvailabilityInput
	Name           string
	Price          int64
	PreviousActive bool
}

func validPOSCatalogTarget(product, revision int64) bool {
	return product > 0 && revision > 0 && revision < 1<<63-1
}

func decodePOSPrice(raw []byte) (posPriceInput, error) {
	var in posPriceInput
	if len(raw) > 512 {
		return in, badInput
	}
	if err := decodeWikiProposalJSON(raw, &in, "product_id", "expected_revision", "price_minor"); err != nil {
		return in, err
	}
	if !validPOSCatalogTarget(in.Product, in.Revision) || in.Price < 1 || in.Price > posPriceMax {
		return in, badInput
	}
	return in, nil
}

func decodePOSAvailability(raw []byte) (posAvailabilityInput, error) {
	var wire struct {
		posAvailabilityInput
		Active *bool `json:"active"`
	}
	if len(raw) > 512 {
		return posAvailabilityInput{}, badInput
	}
	if err := decodeWikiProposalJSON(raw, &wire, "product_id", "expected_revision", "active"); err != nil {
		return posAvailabilityInput{}, err
	}
	in := wire.posAvailabilityInput
	if !validPOSCatalogTarget(in.Product, in.Revision) || wire.Active == nil {
		return in, badInput
	}
	in.Active = *wire.Active
	return in, nil
}

func posCatalogDigest(purpose string, p any) [32]byte {
	raw, _ := json.Marshal(p)
	return sha256.Sum256(append([]byte(purpose+":"), raw...))
}

func (p posPriceProposal) digest() [32]byte {
	return posCatalogDigest(posPriceProposalPurpose, p)
}

func (p posAvailabilityProposal) digest() [32]byte {
	return posCatalogDigest(posAvailabilityProposalPurpose, p)
}

func readPOSPricePreview(ctx context.Context, tx *sql.Tx, s posRenameScope, in posPriceInput) (posPriceProposal, error) {
	if !validPOSCatalogTarget(in.Product, in.Revision) || in.Price < 1 || in.Price > posPriceMax {
		return posPriceProposal{}, badInput
	}
	v, err := readPOSRenamePreview(ctx, tx, s, posRenameInput{Product: in.Product, Revision: in.Revision})
	if err != nil {
		return posPriceProposal{}, err
	}
	if v.PriceMinor == in.Price {
		return posPriceProposal{}, posCatalogNoChange
	}
	return posPriceProposal{s, in, v.CurrentName, v.PriceMinor, v.Active}, nil
}

func readPOSAvailabilityPreview(ctx context.Context, tx *sql.Tx, s posRenameScope, in posAvailabilityInput) (posAvailabilityProposal, error) {
	if !validPOSCatalogTarget(in.Product, in.Revision) {
		return posAvailabilityProposal{}, badInput
	}
	v, err := readPOSRenamePreview(ctx, tx, s, posRenameInput{Product: in.Product, Revision: in.Revision})
	if err != nil {
		return posAvailabilityProposal{}, err
	}
	if v.Active == in.Active {
		return posAvailabilityProposal{}, posCatalogNoChange
	}
	return posAvailabilityProposal{s, in, v.CurrentName, v.PriceMinor, v.Active}, nil
}

// Preparation uses the native outlet lock but does not write catalog data.
func (a *app) preparePOSPrice(ctx context.Context, tx *sql.Tx, s posRenameScope, in posPriceInput) (posPriceProposal, error) {
	if _, err := a.lockPOSRenameScope(ctx, tx, s); err != nil {
		return posPriceProposal{}, err
	}
	return readPOSPricePreview(ctx, tx, s, in)
}

func (a *app) preparePOSAvailability(ctx context.Context, tx *sql.Tx, s posRenameScope, in posAvailabilityInput) (posAvailabilityProposal, error) {
	if _, err := a.lockPOSRenameScope(ctx, tx, s); err != nil {
		return posAvailabilityProposal{}, err
	}
	return readPOSAvailabilityPreview(ctx, tx, s, in)
}

func (a *app) lockPOSCatalogApproval(r *http.Request, tx *sql.Tx, u user, s, approvedScope posRenameScope, actual, approved [32]byte) (entityMembership, error) {
	if u.id == 0 || u.id != s.Actor {
		return entityMembership{}, unauthenticated
	}
	if s != approvedScope || subtle.ConstantTimeCompare(actual[:], approved[:]) != 1 {
		return entityMembership{}, posChangedCatalog
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return entityMembership{}, err
	}
	return a.lockPOSRenameScope(r.Context(), tx, s)
}

// A future browser caller must bind the issuing login, pending proposal and
// confirmation, and store its outcome in this same transaction.
func (a *app) applyPOSPrice(r *http.Request, tx *sql.Tx, u user, s posRenameScope, p posPriceProposal, approved [32]byte) (posProduct, error) {
	m, err := a.lockPOSCatalogApproval(r, tx, u, s, p.Scope, p.digest(), approved)
	if err != nil {
		return posProduct{}, err
	}
	v, err := readPOSPricePreview(r.Context(), tx, s, p.Input)
	if err != nil {
		return posProduct{}, err
	}
	if v != p {
		return posProduct{}, posChangedCatalog
	}
	return savePOSCatalogVersion(r, tx, m, s, v.Input.Product, v.Input.Revision, v.Name, v.Input.Price, v.Active)
}

func (a *app) applyPOSAvailability(r *http.Request, tx *sql.Tx, u user, s posRenameScope, p posAvailabilityProposal, approved [32]byte) (posProduct, error) {
	m, err := a.lockPOSCatalogApproval(r, tx, u, s, p.Scope, p.digest(), approved)
	if err != nil {
		return posProduct{}, err
	}
	v, err := readPOSAvailabilityPreview(r.Context(), tx, s, p.Input)
	if err != nil {
		return posProduct{}, err
	}
	if v != p {
		return posProduct{}, posChangedCatalog
	}
	return savePOSCatalogVersion(r, tx, m, s, v.Input.Product, v.Input.Revision, v.Name, v.Price, v.Input.Active)
}
