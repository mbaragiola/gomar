package main

import (
	"context"
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

type outletDisplayLabelInput struct {
	ExpectedName string `json:"expected_name"`
	Name         string `json:"name"`
}

type outletDisplayLabelPreview struct {
	Scope       reservationConfirmScope `json:"-"`
	OutletID    int64                   `json:"outlet_id"`
	CurrentName string                  `json:"current_name"`
	Name        string                  `json:"name"`
	Active      bool                    `json:"active"`
}

var outletDisplayLabelChanged = problem{409, "El nombre o el estado del local cambió. Prepará otra propuesta."}
var outletDisplayLabelDuplicate = problem{409, "Ya existe un local con ese nombre en la entidad."}
var outletDisplayLabelNoChange = problem{422, "El nombre del local no cambió."}

func validOutletDisplayLabel(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 120 &&
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError }) == -1
}

func decodeOutletDisplayLabel(raw []byte) (outletDisplayLabelInput, error) {
	var in outletDisplayLabelInput
	if len(raw) > 1024 || !utf8.Valid(raw) || decodeWikiProposalJSON(raw, &in, "expected_name", "name") != nil ||
		!validOutletDisplayLabel(in.ExpectedName) || !validOutletDisplayLabel(in.Name) {
		return outletDisplayLabelInput{}, badInput
	}
	if in.ExpectedName == in.Name {
		return outletDisplayLabelInput{}, outletDisplayLabelNoChange
	}
	return in, nil
}

func (p outletDisplayLabelPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   reservationConfirmScope
		Preview outletDisplayLabelPreview
	}{"outlet_display_label_change.v1", p.Scope, p})
	return sha256.Sum256(raw)
}

func (a *app) lockOutletDisplayLabelScope(ctx context.Context, tx *sql.Tx, s reservationConfirmScope) error {
	if !a.outletDisplayLabelProposalsEnabled() {
		return wikiProposalUnavailable
	}
	// Actor/browser or named-key session is already pinned. Native management
	// writers also lock entity first; an exclusive outlet lock pins both the
	// active location and exact current grant for the proposal transaction.
	if err := lockPostgresEntity(ctx, tx, s.Entity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return missingEntity
		}
		return err
	}
	if err := lockReservationConfirmScope(ctx, tx, s); err != nil {
		return err
	}
	_, err := scopedMembership(tx, s.Actor, s.Entity, "manage_entity_access")
	return err
}

// No alias lookup. The outlet is already locked by ID under the named scope.
func readOutletDisplayLabel(ctx context.Context, tx *sql.Tx, s reservationConfirmScope, in outletDisplayLabelInput) (outletDisplayLabelPreview, error) {
	if !validOutletDisplayLabel(in.ExpectedName) || !validOutletDisplayLabel(in.Name) || in.ExpectedName == in.Name {
		return outletDisplayLabelPreview{}, badInput
	}
	p := outletDisplayLabelPreview{Scope: s, OutletID: s.Outlet, Name: in.Name}
	var duplicate bool
	err := tx.QueryRowContext(ctx, `SELECT o.name,o.active,
 EXISTS(SELECT 1 FROM outlets x WHERE x.entity_id=o.entity_id AND x.id<>o.id AND x.name=$3)
 FROM outlets o WHERE o.id=$1 AND o.entity_id=$2 AND o.active`, s.Outlet, s.Entity, in.Name).
		Scan(&p.CurrentName, &p.Active, &duplicate)
	if errors.Is(err, sql.ErrNoRows) {
		return outletDisplayLabelPreview{}, missingOutlet
	}
	if err != nil {
		return outletDisplayLabelPreview{}, err
	}
	if p.CurrentName != in.ExpectedName {
		return outletDisplayLabelPreview{}, outletDisplayLabelChanged
	}
	if duplicate {
		return outletDisplayLabelPreview{}, outletDisplayLabelDuplicate
	}
	return p, nil
}

func (a *app) applyOutletDisplayLabel(r *http.Request, tx *sql.Tx, u user, s reservationConfirmScope, p outletDisplayLabelPreview, approved [32]byte) error {
	if u.id <= 0 || u.id != s.Actor {
		return unauthenticated
	}
	d := p.digest()
	if s != p.Scope || p.OutletID != s.Outlet || !p.Active || subtle.ConstantTimeCompare(d[:], approved[:]) != 1 {
		return outletDisplayLabelChanged
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	ctx := r.Context()
	if err := a.lockOutletDisplayLabelScope(ctx, tx, s); err != nil {
		return err
	}
	fresh, err := readOutletDisplayLabel(ctx, tx, s, outletDisplayLabelInput{ExpectedName: p.CurrentName, Name: p.Name})
	if err != nil {
		return err
	}
	if fresh != p {
		return outletDisplayLabelChanged
	}
	var after tenantOutletSnapshot
	err = tx.QueryRowContext(ctx, `UPDATE outlets SET name=$1 WHERE id=$2 AND entity_id=$3 AND name=$4 AND active
		RETURNING name,active`, p.Name, s.Outlet, s.Entity, p.CurrentName).Scan(&after.Name, &after.Active)
	if isUnique(err) {
		return outletDisplayLabelDuplicate
	}
	if errors.Is(err, sql.ErrNoRows) {
		return outletDisplayLabelChanged
	}
	if err != nil {
		return err
	}
	return recordTenantCatalogEvent(ctx, tx, s.Entity, u.id, sql.NullInt64{Int64: s.Outlet, Valid: true},
		"update_outlet", tenantOutletSnapshot{fresh.CurrentName, fresh.Active}, after)
}
