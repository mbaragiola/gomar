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

type reservationCancelScope struct{ Actor, Entity, Outlet int64 }

type reservationCancelInput struct {
	Reference string `json:"reservation_reference"`
}

type reservationCancelPreview struct {
	Scope      reservationCancelScope `json:"-"`
	Reference  string                 `json:"reservation_reference"`
	StartUTC   string                 `json:"start_utc"`
	StartLocal string                 `json:"start_local"`
	Timezone   string                 `json:"timezone"`
	PartySize  int                    `json:"party_size"`
	Status     string                 `json:"status"`
	TableLabel string                 `json:"table_label,omitempty"`
}

func decodeReservationCancel(raw []byte) (reservationCancelInput, error) {
	var in reservationCancelInput
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

func (p reservationCancelPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   reservationCancelScope
		Preview reservationCancelPreview
	}{"reservation_cancel.v1", p.Scope, p})
	return sha256.Sum256(raw)
}

func prepareReservationCancel(ctx context.Context, tx *sql.Tx, s reservationCancelScope, in reservationCancelInput, now time.Time) (reservationCancelPreview, error) {
	if s.Actor <= 0 || s.Entity <= 0 || s.Outlet <= 0 || !reservationID.MatchString(in.Reference) {
		return reservationCancelPreview{}, badInput
	}
	m, err := scopedMembership(tx, s.Actor, s.Entity, "")
	if err != nil {
		return reservationCancelPreview{}, err
	}
	if err = requireReservationStaff(ctx, tx, m, s.Outlet); err != nil {
		return reservationCancelPreview{}, err
	}
	p := reservationCancelPreview{Scope: s, Reference: in.Reference, Timezone: reservationTimezone}
	err = tx.QueryRowContext(ctx, `SELECT start_at,party_size,status,coalesce(table_label,'')
 FROM reservation_requests WHERE outlet_id=$1 AND id=$2`, s.Outlet, in.Reference).Scan(&p.StartUTC, &p.PartySize, &p.Status, &p.TableLabel)
	if errors.Is(err, sql.ErrNoRows) {
		return reservationCancelPreview{}, reservationMissing
	}
	if err != nil {
		return reservationCancelPreview{}, err
	}
	start, err := time.Parse(time.RFC3339, p.StartUTC)
	if err != nil || p.StartUTC != start.UTC().Format(time.RFC3339) || !start.After(now) ||
		(p.Status != "pending" && p.Status != "confirmed") || (p.Status == "confirmed") != (p.TableLabel != "") {
		return reservationCancelPreview{}, reservationStateConflict
	}
	p.StartLocal = start.In(reservationLocation).Format(time.RFC3339)
	return p, nil
}

// A future browser handler must lock the exact issuing login and pending
// proposal and store its terminal receipt in this same transaction. A
// completed retry should return that receipt, never replay cancellation.
func applyReservationCancel(r *http.Request, tx *sql.Tx, u user, s reservationCancelScope, p reservationCancelPreview, approved [32]byte, clock func() time.Time) (reservationResult, error) {
	if u.id <= 0 || u.id != s.Actor {
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
	if err := lockReservationCancelScope(ctx, tx, s); err != nil {
		return reservationResult{}, err
	}
	fresh, err := prepareReservationCancel(ctx, tx, s, reservationCancelInput{p.Reference}, clock())
	if err != nil {
		return reservationResult{}, err
	}
	if fresh != p {
		return reservationResult{}, reservationStateConflict
	}
	return cancelReservationSnapshot(ctx, tx, s.Outlet, p.Reference, p.Status, p.StartUTC, p.PartySize, p.TableLabel)
}

func lockReservationCancelScope(ctx context.Context, tx *sql.Tx, s reservationCancelScope) error {
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
