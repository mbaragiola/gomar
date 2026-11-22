package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const posRenameProposalPurpose = "pos_product_rename.v1"

var posRenameUnavailable = problem{503, "El catálogo de caja no está habilitado."}
var posRenameNoChange = problem{422, "El nombre del producto no cambió."}

type posRenameScope struct {
	Actor, Entity, Outlet int64
}

type posRenameInput struct {
	Product  int64  `json:"product_id"`
	Revision int64  `json:"expected_revision"`
	Name     string `json:"name"`
}

type posRenamePreview struct {
	posRenameInput
	CurrentName string `json:"current_name"`
	PriceMinor  int64  `json:"unchanged_price_minor"`
	Active      bool   `json:"unchanged_active"`
}

type posRenameProposal struct {
	Scope   posRenameScope
	Preview posRenamePreview
}

func decodePOSRename(raw []byte) (posRenameInput, error) {
	var input posRenameInput
	if len(raw) > 2048 {
		return input, badInput
	}
	if err := decodePayables(raw, &input, "product_id", "expected_revision", "name"); err != nil {
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
	return normalizePOSRename(input)
}

func normalizePOSRename(input posRenameInput) (posRenameInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Product <= 0 || input.Revision <= 0 || input.Revision == 1<<63-1 || input.Name == "" ||
		!utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) > 120 || strings.ContainsRune(input.Name, 0) {
		return input, badInput
	}
	return input, nil
}

func (p posRenameProposal) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   posRenameScope
		Preview posRenamePreview
	}{posRenameProposalPurpose, p.Scope, p.Preview})
	return sha256.Sum256(raw)
}

func (a *app) lockPOSRenameScope(ctx context.Context, tx *sql.Tx, s posRenameScope) (entityMembership, error) {
	var empty entityMembership
	if !a.enablePOS {
		return empty, posRenameUnavailable
	}
	if s.Actor <= 0 || s.Entity <= 0 || s.Outlet <= 0 {
		return empty, badInput
	}
	// Match entity administration's ordering. Acquire the outlet's native
	// write lock before any lesser outlet lock; POS sales share that lock.
	var entityID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM entities WHERE id=$1 AND active FOR SHARE`, s.Entity).Scan(&entityID)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, missingEntity
	}
	if err != nil {
		return empty, err
	}
	var membershipID int64
	var role string
	err = tx.QueryRowContext(ctx, `SELECT m.id,m.role FROM memberships m JOIN users u ON u.id=m.user_id AND u.active
		WHERE m.entity_id=$1 AND m.user_id=$2 AND m.active FOR SHARE OF m`, s.Entity, s.Actor).Scan(&membershipID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, missingEntity
	}
	if err != nil {
		return empty, err
	}
	if role != "administration" {
		return empty, forbiddenRole
	}
	m, err := scopedMembership(tx, s.Actor, s.Entity, "manage_entity_access")
	if err != nil {
		return empty, err
	}
	var outlet int64
	err = tx.QueryRowContext(ctx, `SELECT o.id FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id
		WHERE o.id=$1 AND o.entity_id=$2 AND o.active AND g.membership_id=$3 FOR UPDATE OF o FOR SHARE OF g`,
		s.Outlet, s.Entity, membershipID).Scan(&outlet)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, missingOutlet
	}
	return m, err
}

func readPOSRenamePreview(ctx context.Context, tx *sql.Tx, s posRenameScope, in posRenameInput) (posRenamePreview, error) {
	p := posRenamePreview{posRenameInput: in}
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT p.current_revision,v.name,v.price_minor,v.active
		FROM pos_products p JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
		WHERE p.id=$1 AND p.outlet_id=$2`, in.Product, s.Outlet).Scan(&revision, &p.CurrentName, &p.PriceMinor, &p.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return p, posMissingProduct
	}
	if err == nil && revision != in.Revision {
		return p, posChangedCatalog
	}
	return p, err
}

func (a *app) preparePOSRename(ctx context.Context, tx *sql.Tx, s posRenameScope, input posRenameInput) (posRenameProposal, error) {
	var empty posRenameProposal
	input, err := normalizePOSRename(input)
	if err != nil {
		return empty, err
	}
	if _, err = a.lockPOSRenameScope(ctx, tx, s); err != nil {
		return empty, err
	}
	p, err := readPOSRenamePreview(ctx, tx, s, input)
	if err != nil {
		return empty, err
	}
	if p.Name == p.CurrentName {
		return empty, posRenameNoChange
	}
	return posRenameProposal{s, p}, nil
}

// Call only after same-actor browser review, origin/CSRF, key-purpose and
// expiry checks. Save the terminal actor receipt in this same transaction.
func (a *app) applyPOSRename(r *http.Request, tx *sql.Tx, u user, s posRenameScope, p posRenameProposal, confirmed [32]byte) (posProduct, error) {
	var empty posProduct
	if u.id == 0 || u.id != s.Actor {
		return empty, unauthenticated
	}
	digest := p.digest()
	if p.Scope != s || subtle.ConstantTimeCompare(digest[:], confirmed[:]) != 1 {
		return empty, posChangedCatalog
	}
	input, err := normalizePOSRename(p.Preview.posRenameInput)
	if err != nil {
		return empty, posChangedCatalog
	}
	m, err := a.lockPOSRenameScope(r.Context(), tx, s)
	if err != nil {
		return empty, err
	}
	active, err := currentUser(nil, r, tx)
	if err != nil {
		return empty, err
	}
	if active.id != u.id {
		return empty, unauthenticated
	}
	fresh, err := readPOSRenamePreview(r.Context(), tx, s, input)
	if err != nil {
		return empty, err
	}
	if fresh != p.Preview || fresh.CurrentName == input.Name {
		return empty, posChangedCatalog
	}

	return savePOSCatalogVersion(r, tx, m, s, input.Product, input.Revision, input.Name, fresh.PriceMinor, fresh.Active)
}

func savePOSCatalogVersion(r *http.Request, tx *sql.Tx, m entityMembership, s posRenameScope, product, revision int64, name string, price int64, active bool) (posProduct, error) {
	var empty posProduct
	write := r.Clone(r.Context())
	write.Method = http.MethodPut
	write.SetPathValue("outlet", strconv.FormatInt(s.Outlet, 10))
	write.SetPathValue("product", strconv.FormatInt(product, 10))
	raw, err := json.Marshal(struct {
		Name     string `json:"nombre"`
		Price    int64  `json:"precio_centavos"`
		Active   bool   `json:"activo"`
		Revision int64  `json:"revision"`
	}{name, price, active, revision})
	if err != nil {
		return empty, err
	}
	_, saved, err := savePOSProduct(write, tx, m, raw)
	if err != nil {
		return empty, err
	}
	return saved.(posProduct), nil
}
