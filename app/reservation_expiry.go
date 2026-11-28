package main

import (
	"context"
	"log"
	"time"
)

const (
	reservationExpiryInterval = time.Minute
	reservationExpiryBatch    = 128
	reservationExpiryTimeout  = 2 * time.Second
)

func (a *app) reservationExpiryLoop(ctx context.Context) {
	ticker := time.NewTicker(reservationExpiryInterval)
	defer ticker.Stop()
	for {
		if err := a.expirePendingReservations(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Print("could not expire pending reservations; will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *app) expirePendingReservations(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, reservationExpiryTimeout)
	defer cancel()
	_, err := a.db.ExecContext(ctx, `WITH stale AS (
		SELECT id FROM reservation_requests WHERE status='pending' AND start_at <= $1
		ORDER BY start_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
	)
	UPDATE reservation_requests r SET status='expired'
	FROM stale WHERE r.id=stale.id AND r.status='pending'`,
		now.UTC().Format(time.RFC3339), reservationExpiryBatch)
	return err
}
