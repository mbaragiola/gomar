package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

type supplierNameInput struct {
	ID       int64  `json:"supplier_id"`
	Revision int64  `json:"expected_revision"`
	Name     string `json:"name"`
}

type supplierNamePreview struct {
	Scope documentReviewScope `json:"-"`
	supplierNameInput
	CurrentName  string `json:"current_name"`
	BusinessWide bool   `json:"business_wide"`
}

var supplierNameStale = problem{409, "Cambió el proveedor. Prepará otra propuesta."}
var supplierNameNoChange = problem{422, "La razón social no cambió."}

func decodeSupplierName(raw []byte) (supplierNameInput, error) {
	var in supplierNameInput
	if len(raw) > 2048 {
		return in, badInput
	}
	if err := decodeWikiProposalJSON(raw, &in, "supplier_id", "expected_revision", "name"); err != nil {
		return in, err
	}
	return normalizeSupplierName(in)
}

func normalizeSupplierName(in supplierNameInput) (supplierNameInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.ID < 1 || in.Revision < 1 || in.Revision == 1<<63-1 || in.Name == "" || !utf8.ValidString(in.Name) ||
		utf8.RuneCountInString(in.Name) > 160 || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return in, badInput
	}
	return in, nil
}

func (p supplierNamePreview) digest() [32]byte {
	data, _ := json.Marshal(struct {
		Purpose string
		Scope   documentReviewScope
		Preview supplierNamePreview
	}{"supplier_name.v1", p.Scope, p})
	return sha256.Sum256(data)
}

func prepareSupplierName(r *http.Request, tx *sql.Tx, s documentReviewScope, in supplierNameInput) (supplierNamePreview, error) {
	in, err := normalizeSupplierName(in)
	if err != nil || s.Actor < 1 || s.Entity < 1 || s.Outlet < 1 {
		return supplierNamePreview{}, badInput
	}
	m, err := scopedMembership(tx, s.Actor, s.Entity, "manage_suppliers")
	if err != nil {
		return supplierNamePreview{}, err
	}
	if err = requireSelectedDocumentOutlet(tx, m, s.Outlet); err != nil {
		return supplierNamePreview{}, err
	}
	p := supplierNamePreview{Scope: s, supplierNameInput: in, BusinessWide: true}
	var revision int64
	err = tx.QueryRowContext(r.Context(), `SELECT name,revision FROM suppliers WHERE id=$1 AND entity_id=$2 AND active`,
		in.ID, s.Entity).Scan(&p.CurrentName, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return supplierNamePreview{}, problem{404, "No se encontró el proveedor."}
	}
	if err != nil {
		return supplierNamePreview{}, err
	}
	if revision != in.Revision {
		return supplierNamePreview{}, supplierNameStale
	}
	if p.CurrentName == in.Name {
		return supplierNamePreview{}, supplierNameNoChange
	}
	return p, nil
}

// Call only with the issuing browser login, a locked pending proposal and
// explicit business-wide approval. The future proposal receipt should store
// only actor, entity, authorization outlet, supplier ID, resulting revision,
// decision and time (never identifiers from the supplier's tax record).
func applySupplierName(r *http.Request, tx *sql.Tx, u user, s documentReviewScope, p supplierNamePreview, confirmed [32]byte) (int64, error) {
	if u.id < 1 || u.id != s.Actor {
		return 0, unauthenticated
	}
	d := p.digest()
	if p.Scope != s || !p.BusinessWide || subtle.ConstantTimeCompare(d[:], confirmed[:]) != 1 {
		return 0, supplierNameStale
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return 0, err
	}
	ctx := r.Context()
	if err := lockPostgresEntity(ctx, tx, s.Entity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, missingEntity
		}
		return 0, err
	}
	if err := lockScopedDocumentCapability(ctx, tx, s, "manage_suppliers"); err != nil {
		return 0, err
	}
	fresh, err := prepareSupplierName(r, tx, s, p.supplierNameInput)
	if err != nil {
		return 0, err
	}
	if fresh != p {
		return 0, supplierNameStale
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `UPDATE suppliers SET name=$1
 WHERE id=$2 AND entity_id=$3 AND active AND revision=$4 AND name=$5 RETURNING revision`,
		fresh.Name, fresh.ID, s.Entity, fresh.Revision, fresh.CurrentName).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, supplierNameStale
	}
	if err != nil {
		return 0, err
	}
	if revision != fresh.Revision+1 {
		return 0, errors.New("supplier name revision did not advance")
	}
	return revision, nil
}
