package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type reservationSlotAddInput struct {
	StartAt string `json:"start_at"`
}

type reservationSlotAddPreview struct {
	Scope        reservationConfirmScope `json:"-"`
	StartAt      string                  `json:"start_at"`
	StartLocal   string                  `json:"start_local"`
	Timezone     string                  `json:"timezone"`
	VenueEnabled bool                    `json:"venue_enabled"`
}

var reservationSlotAddChanged = problem{409, "Cambió la configuración del local o el turno ya existe. Prepará otra propuesta."}
var reservationSlotAddEnabled = problem{409, "Solo se puede preparar un turno para un local de reservas desactivado."}
var reservationSlotAddElapsed = problem{422, "El turno debe empezar en el futuro."}
var reservationSlotAddAcknowledgment = problem{422, "Confirmá que Gomar no permite retirar este turno y que podría estar disponible al habilitar reservas."}

func parseReservationSlotAddStart(raw string) (time.Time, error) {
	stamp, err := time.Parse(time.RFC3339, raw)
	if err != nil || stamp.UTC().Format(time.RFC3339) != raw || stamp.Second() != 0 {
		return time.Time{}, badInput
	}
	return stamp, nil
}

func decodeReservationSlotAdd(raw []byte) (reservationSlotAddInput, error) {
	var in reservationSlotAddInput
	if len(raw) > 256 || decodeWikiProposalJSON(raw, &in, "start_at") != nil {
		return reservationSlotAddInput{}, badInput
	}
	_, err := parseReservationSlotAddStart(in.StartAt)
	return in, err
}

func (p reservationSlotAddPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   reservationConfirmScope
		Preview reservationSlotAddPreview
	}{"reservation_slot_add.v1", p.Scope, p})
	return sha256.Sum256(raw)
}

func (a *app) lockReservationSlotAddScope(ctx context.Context, tx *sql.Tx, s reservationConfirmScope) error {
	if !a.reservationSlotAddProposalsEnabled() {
		return wikiProposalUnavailable
	}
	return lockReservationConfirmScope(ctx, tx, s)
}

// Caller holds the outlet lock shared by native slot writes and venue
// settings. Reuse the native time grid and horizon for both decisions.
func readReservationSlotAdd(ctx context.Context, tx *sql.Tx, s reservationConfirmScope, in reservationSlotAddInput, now time.Time) (reservationSlotAddPreview, error) {
	stamp, err := parseReservationSlotAddStart(in.StartAt)
	if err != nil {
		return reservationSlotAddPreview{}, err
	}
	if !stamp.After(now) {
		return reservationSlotAddPreview{}, reservationSlotAddElapsed
	}
	if _, err := reservationStart(in.StartAt, now); err != nil {
		return reservationSlotAddPreview{}, err
	}
	p := reservationSlotAddPreview{Scope: s, StartAt: in.StartAt, StartLocal: stamp.In(reservationLocation).Format(time.RFC3339), Timezone: reservationTimezone}
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT v.enabled,
 EXISTS(SELECT 1 FROM reservation_slots s WHERE s.outlet_id=v.outlet_id AND s.start_at=$2)
 FROM reservation_venues v WHERE v.outlet_id=$1`, s.Outlet, p.StartAt).Scan(&p.VenueEnabled, &exists)
	if errors.Is(err, sql.ErrNoRows) {
		return reservationSlotAddPreview{}, reservationUnavailable
	}
	if err != nil {
		return reservationSlotAddPreview{}, err
	}
	if p.VenueEnabled {
		return reservationSlotAddPreview{}, reservationSlotAddEnabled
	}
	if exists {
		return reservationSlotAddPreview{}, reservationSlotAddChanged
	}
	return p, nil
}

func (a *app) applyReservationSlotAdd(r *http.Request, tx *sql.Tx, u user, s reservationConfirmScope, p reservationSlotAddPreview, approved [32]byte) error {
	if u.id <= 0 || u.id != s.Actor {
		return unauthenticated
	}
	d := p.digest()
	if s != p.Scope || p.VenueEnabled || subtle.ConstantTimeCompare(d[:], approved[:]) != 1 {
		return reservationSlotAddChanged
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	ctx := r.Context()
	if err := a.lockReservationSlotAddScope(ctx, tx, s); err != nil {
		return err
	}
	fresh, err := readReservationSlotAdd(ctx, tx, s, reservationSlotAddInput{p.StartAt}, a.tokenTime())
	if err != nil && err != reservationUnavailable && err != reservationSlotAddEnabled && err != reservationSlotAddChanged && err != reservationSlotAddElapsed && err != badInput {
		return err
	}
	if err != nil || fresh != p {
		return reservationSlotAddChanged
	}
	if _, err := reservationStart(p.StartAt, a.tokenTime()); err != nil {
		return reservationSlotAddChanged
	}
	return insertReservationSlot(ctx, tx, s.Outlet, p.StartAt)
}
