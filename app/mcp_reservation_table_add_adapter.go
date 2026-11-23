package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

type reservationTableAddInput struct {
	TableNumber int `json:"table_number"`
	Capacity    int `json:"capacity"`
}

type reservationTableAddPreview struct {
	Scope        reservationConfirmScope `json:"-"`
	TableNumber  int                     `json:"table_number"`
	Label        string                  `json:"table_label"`
	Capacity     int                     `json:"capacity"`
	VenueEnabled bool                    `json:"venue_enabled"`
}

var reservationTableAddChanged = problem{409, "Cambió la configuración del local o ya existe esa mesa. Prepará otra propuesta."}
var reservationTableAddEnabled = problem{409, "Solo se puede preparar una mesa para un local de reservas desactivado."}
var reservationTableAddAcknowledgment = problem{422, "Confirmá que Gomar no permite retirar esta mesa y que podría ampliar la disponibilidad al habilitar reservas."}

func validReservationTableNumber(number int) bool { return number >= 1 && number <= 9999 }

// Match the same numbered Mesa label, including legacy case, spaces and leading zeros.
func reservationTableNumberPattern(number int) string {
	return "^[[:space:]]*mesa[[:space:]]+0*" + strconv.Itoa(number) + "[[:space:]]*$"
}

func validReservationTableAdd(in reservationTableAddInput) bool {
	return validReservationTableNumber(in.TableNumber) && in.Capacity >= 1 && in.Capacity <= 12
}

func decodeReservationTableAdd(raw []byte) (reservationTableAddInput, error) {
	var in reservationTableAddInput
	if len(raw) > 384 || decodeWikiProposalJSON(raw, &in, "table_number", "capacity") != nil || !validReservationTableAdd(in) {
		return reservationTableAddInput{}, badInput
	}
	return in, nil
}

func (p reservationTableAddPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   reservationConfirmScope
		Preview reservationTableAddPreview
	}{"reservation_table_add.v1", p.Scope, p})
	return sha256.Sum256(raw)
}

func (a *app) lockReservationTableAddScope(ctx context.Context, tx *sql.Tx, s reservationConfirmScope) error {
	if !a.reservationTableAddProposalsEnabled() {
		return wikiProposalUnavailable
	}
	return lockReservationConfirmScope(ctx, tx, s)
}

// The caller holds the same outlet lock as native table adds and reservation
// settings writes. Neither path can enable a venue while this read/write runs.
func readReservationTableAdd(ctx context.Context, tx *sql.Tx, s reservationConfirmScope, in reservationTableAddInput) (reservationTableAddPreview, error) {
	if !validReservationTableAdd(in) {
		return reservationTableAddPreview{}, badInput
	}
	number := strconv.Itoa(in.TableNumber)
	p := reservationTableAddPreview{Scope: s, TableNumber: in.TableNumber, Label: "Mesa " + number, Capacity: in.Capacity}
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT v.enabled,
 EXISTS(SELECT 1 FROM reservation_tables t WHERE t.outlet_id=v.outlet_id
   AND lower(t.label) ~ $2)
 FROM reservation_venues v WHERE v.outlet_id=$1`, s.Outlet, reservationTableNumberPattern(in.TableNumber)).Scan(&p.VenueEnabled, &exists)
	if errors.Is(err, sql.ErrNoRows) {
		return reservationTableAddPreview{}, reservationUnavailable
	}
	if err != nil {
		return reservationTableAddPreview{}, err
	}
	if p.VenueEnabled {
		return reservationTableAddPreview{}, reservationTableAddEnabled
	}
	if exists {
		return reservationTableAddPreview{}, reservationTableAddChanged
	}
	return p, nil
}

func (a *app) applyReservationTableAdd(r *http.Request, tx *sql.Tx, u user, s reservationConfirmScope, p reservationTableAddPreview, approved [32]byte) error {
	if u.id <= 0 || u.id != s.Actor {
		return unauthenticated
	}
	d := p.digest()
	if s != p.Scope || p.VenueEnabled || subtle.ConstantTimeCompare(d[:], approved[:]) != 1 {
		return reservationTableAddChanged
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	ctx := r.Context()
	if err := a.lockReservationTableAddScope(ctx, tx, s); err != nil {
		return err
	}
	fresh, err := readReservationTableAdd(ctx, tx, s, reservationTableAddInput{p.TableNumber, p.Capacity})
	if err != nil && err != reservationUnavailable && err != reservationTableAddEnabled && err != reservationTableAddChanged {
		return err
	}
	if err != nil || fresh != p {
		return reservationTableAddChanged
	}
	return insertReservationTable(ctx, tx, s.Outlet, reservationTable{Label: p.Label, Capacity: p.Capacity})
}
