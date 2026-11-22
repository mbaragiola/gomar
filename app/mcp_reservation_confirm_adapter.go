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

type reservationConfirmScope struct{ Actor, Entity, Outlet int64 }

type reservationConfirmInput struct {
	Reference string `json:"reservation_reference"`
}

type reservationConfirmPreview struct {
	Scope         reservationConfirmScope `json:"-"`
	Reference     string                  `json:"reservation_reference"`
	StartUTC      string                  `json:"start_utc"`
	StartLocal    string                  `json:"start_local"`
	Timezone      string                  `json:"timezone"`
	PartySize     int                     `json:"party_size"`
	Status        string                  `json:"status"`
	TableLabel    string                  `json:"table_label"`
	TableCapacity int                     `json:"table_capacity"`
}

func decodeReservationConfirm(raw []byte) (reservationConfirmInput, error) {
	var in reservationConfirmInput
	if len(raw) > 256 {
		return in, badInput
	}
	if err := decodeWikiProposalJSON(raw, &in, "reservation_reference"); err != nil {
		return in, err
	}
	if !reservationID.MatchString(in.Reference) {
		return in, badInput
	}
	return in, nil
}

func (p reservationConfirmPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   reservationConfirmScope
		Preview reservationConfirmPreview
	}{"reservation_confirm.v1", p.Scope, p})
	return sha256.Sum256(raw)
}

func prepareReservationConfirm(ctx context.Context, tx *sql.Tx, s reservationConfirmScope, in reservationConfirmInput, now time.Time) (reservationConfirmPreview, error) {
	if s.Actor <= 0 || s.Entity <= 0 || s.Outlet <= 0 || !reservationID.MatchString(in.Reference) {
		return reservationConfirmPreview{}, badInput
	}
	member, err := scopedMembership(tx, s.Actor, s.Entity, "")
	if err != nil {
		return reservationConfirmPreview{}, err
	}
	if err = requireReservationStaff(ctx, tx, member, s.Outlet); err != nil {
		return reservationConfirmPreview{}, err
	}
	var p = reservationConfirmPreview{Scope: s, Reference: in.Reference, Timezone: reservationTimezone}
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT r.start_at,r.party_size,r.status,v.enabled
 FROM reservation_requests r JOIN reservation_venues v ON v.outlet_id=r.outlet_id
 WHERE r.outlet_id=$1 AND r.id=$2`, s.Outlet, in.Reference).Scan(&p.StartUTC, &p.PartySize, &p.Status, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return reservationConfirmPreview{}, reservationMissing
	}
	if err != nil {
		return reservationConfirmPreview{}, err
	}
	start, err := time.Parse(time.RFC3339, p.StartUTC)
	if err != nil || p.StartUTC != start.UTC().Format(time.RFC3339) || !start.After(now) || !enabled || p.Status != "pending" {
		return reservationConfirmPreview{}, reservationStateConflict
	}
	p.StartLocal = start.In(reservationLocation).Format(time.RFC3339)
	p.TableLabel, p.TableCapacity, err = pickReservationTableWithCapacity(ctx, tx, s.Outlet, p.StartUTC, p.PartySize)
	return p, err
}

// The caller must bind the issuing login and pending proposal, validate
// explicit browser approval, and consume the proposal in this transaction.
func applyReservationConfirm(r *http.Request, tx *sql.Tx, u user, s reservationConfirmScope, p reservationConfirmPreview, approved [32]byte, clock func() time.Time) (reservationResult, error) {
	if u.id == 0 || u.id != s.Actor {
		return reservationResult{}, unauthenticated
	}
	digest := p.digest()
	if s != p.Scope || subtle.ConstantTimeCompare(digest[:], approved[:]) != 1 {
		return reservationResult{}, reservationStateConflict
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return reservationResult{}, err
	}
	ctx := r.Context()
	if err := lockReservationConfirmScope(ctx, tx, s); err != nil {
		return reservationResult{}, err
	}
	fresh, err := prepareReservationConfirm(ctx, tx, s, reservationConfirmInput{p.Reference}, clock())
	if err != nil {
		return reservationResult{}, err
	}
	if fresh != p {
		return reservationResult{}, reservationStateConflict
	}
	return confirmReservationTable(ctx, tx, s.Outlet, p.Reference, p.StartUTC, p.PartySize, p.TableLabel)
}

func lockReservationConfirmScope(ctx context.Context, tx *sql.Tx, s reservationConfirmScope) error {
	var membership int64
	var role string
	err := tx.QueryRowContext(ctx, `SELECT m.id,m.role FROM memberships m JOIN entities e ON e.id=m.entity_id AND e.active
 WHERE m.entity_id=$1 AND m.user_id=$2 AND m.active FOR SHARE OF m,e`, s.Entity, s.Actor).Scan(&membership, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return missingEntity
	}
	if err != nil {
		return err
	}
	if role != "administration" {
		return forbiddenRole
	}
	var outlet int64
	err = tx.QueryRowContext(ctx, `SELECT o.id FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id AND g.membership_id=$1
 WHERE o.entity_id=$2 AND o.id=$3 AND o.active FOR UPDATE OF o FOR SHARE OF g`, membership, s.Entity, s.Outlet).Scan(&outlet)
	if errors.Is(err, sql.ErrNoRows) {
		return missingOutlet
	}
	return err
}
