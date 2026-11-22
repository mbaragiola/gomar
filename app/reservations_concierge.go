package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mbaragiola/gomar/app/internal/publicconcierge"
)

type reservationConciergeCatalog struct {
	service reservationsService
	key     [32]byte
	now     func() time.Time
	enabled bool
}

func (c reservationConciergeCatalog) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func conciergeReservationError(err error) error {
	switch {
	case errors.Is(err, reservationUnavailable):
		return publicconcierge.ErrClosed
	case errors.Is(err, badInput), errors.Is(err, reservationConflict), errors.Is(err, reservationQueueFull):
		return publicconcierge.ErrUnavailable
	default:
		return err
	}
}

func (c reservationConciergeCatalog) Info(ctx context.Context, slug string) (publicconcierge.VenueInfo, error) {
	if !c.enabled {
		return publicconcierge.VenueInfo{}, publicconcierge.ErrClosed
	}
	tx, err := c.service.db.BeginTx(ctx, nil)
	if err != nil {
		return publicconcierge.VenueInfo{}, err
	}
	defer tx.Rollback()
	_, v, err := reservationPublicVenue(ctx, tx, slug)
	if err != nil {
		return publicconcierge.VenueInfo{}, conciergeReservationError(err)
	}
	menu, err := reservationMenuForConcierge(ctx, tx, slug, true)
	if err != nil {
		return publicconcierge.VenueInfo{}, err
	}
	return publicconcierge.VenueInfo{Policy: v.CancellationPolicy, Menu: menu}, nil
}

func (c reservationConciergeCatalog) Available(ctx context.Context, slug, date string, n int) ([]time.Time, error) {
	if !c.enabled {
		return nil, publicconcierge.ErrClosed
	}
	slots, err := c.service.availability(ctx, slug, date, n, c.currentTime())
	if err != nil {
		return nil, conciergeReservationError(err)
	}
	result := make([]time.Time, 0, len(slots))
	for _, slot := range slots {
		at, err := time.Parse(time.RFC3339, slot.StartAt)
		if err != nil {
			return nil, err
		}
		result = append(result, at)
	}
	return result, nil
}

func conciergeEventDigest(key [32]byte, outlet int64, id string) []byte {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte("gomar/public-reservation/v1\x00" + strconv.FormatInt(outlet, 10) + "\x00" + id))
	return mac.Sum(nil)
}

func (c reservationConciergeCatalog) Request(ctx context.Context, slug string, in publicconcierge.Request) (publicconcierge.Pending, error) {
	var out publicconcierge.Pending
	if !c.enabled {
		return out, publicconcierge.ErrClosed
	}
	if c.key == ([32]byte{}) {
		return out, errors.New("public concierge key not configured")
	}
	name, valid := reservationText(in.GuestName, 1, 80)
	if !valid || in.PartySize < 1 || in.PartySize > 12 || !validConciergeEventID(in.EventID) {
		return out, publicconcierge.ErrUnavailable
	}
	tx, err := c.service.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	outlet, err := c.service.lockedVenue(ctx, tx, slug)
	if err != nil {
		return out, conciergeReservationError(err)
	}
	digest := conciergeEventDigest(c.key, outlet, in.EventID)
	var storedName, startAt string
	var count int
	err = tx.QueryRowContext(ctx, `SELECT r.id,r.start_at,r.guest_name,r.party_size
  FROM reservation_concierge_events e JOIN reservation_requests r ON r.id=e.reservation_id AND r.outlet_id=e.outlet_id
  WHERE e.outlet_id=$1 AND e.fingerprint=$2`, outlet, digest).Scan(&out.Reference, &startAt, &storedName, &count)
	switch {
	case err == nil:
		if startAt != in.StartAt.UTC().Format(time.RFC3339) || in.StartAt.Nanosecond() != 0 || storedName != name || count != in.PartySize {
			return publicconcierge.Pending{}, publicconcierge.ErrUnavailable
		}
		out.Replay = true
		return out, nil
	case !errors.Is(err, sql.ErrNoRows):
		return out, err
	}
	now := c.currentTime()
	stamp, err := reservationStart(in.StartAt.Format(time.RFC3339Nano), now)
	if err != nil {
		return out, conciergeReservationError(err)
	}
	result, err := requestReservationInTx(ctx, tx, outlet, stamp, name, in.PartySize, now)
	if err != nil {
		return out, conciergeReservationError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO reservation_concierge_events(outlet_id,fingerprint,reservation_id) VALUES($1,$2,$3)`, outlet, digest, result.ID); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return publicconcierge.Pending{Reference: result.ID}, nil
}

func validConciergeEventID(id string) bool {
	if len(id) < 1 || len(id) > 100 {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) < 0
}

// runReservationConcierge is a local integration seam, not an HTTP handler.
// No real sink, provider, route, background job, or key loader is registered.
func (a *app) runReservationConcierge(ctx context.Context, enabled bool, key [32]byte, now func() time.Time, origin string, sink publicconcierge.Sink, in publicconcierge.Intent) error {
	catalog := reservationConciergeCatalog{reservationsService{db: a.db}, key, now, enabled}
	return (publicconcierge.Concierge{Catalog: catalog, Sink: sink, PublicOrigin: origin}).Handle(ctx, in)
}
