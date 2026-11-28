package main

import (
	"errors"
	"testing"
	"time"
)

func TestPostgresPublishedReservationScope(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const fixture = `
	INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fictional Bakery','00000000001',TRUE),(2,'Fictional Cafe','00000000002',TRUE);
	INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'North internal',TRUE),(2,1,'West internal',TRUE),
		(3,2,'East internal',TRUE),(4,2,'South internal',TRUE);
	INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled)
	VALUES(1,'fiction-bakery-north','Bakery North','',TRUE),(2,'fiction-bakery-west','Bakery West','',TRUE),
		(3,'fiction-cafe-east','Cafe East','',TRUE),(4,'fiction-cafe-south','Cafe South','',FALSE);
	INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(1,'Mesa 1',4),(2,'Mesa 1',4),(3,'Mesa 1',4),(4,'Mesa 1',4);
	INSERT INTO reservation_slots(outlet_id,start_at) VALUES(1,'2030-11-20T15:00:00Z'),(2,'2030-11-20T16:30:00Z'),
		(3,'2030-11-20T18:00:00Z'),(4,'2030-11-20T19:30:00Z');`
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 11, 19, 12, 0, 0, 0, time.UTC)
	scope := func(entity, outlet int64, venue string) reservationsService {
		t.Helper()
		svc, err := publishedReservations(db, reservationPublication{entity: entity, outlet: outlet, venue: venue})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	expectUnavailable := func(err error) {
		t.Helper()
		if !errors.Is(err, reservationUnavailable) {
			t.Fatalf("expected guest-safe denial, got %v", err)
		}
	}
	for _, tc := range []struct {
		entity, outlet     int64
		venue, name, start string
	}{
		{1, 1, "fiction-bakery-north", "Bakery North", "2030-11-20T15:00:00Z"},
		{1, 2, "fiction-bakery-west", "Bakery West", "2030-11-20T16:30:00Z"},
		{2, 3, "fiction-cafe-east", "Cafe East", "2030-11-20T18:00:00Z"},
	} {
		svc := scope(tc.entity, tc.outlet, tc.venue)
		info, err := svc.info(ctx, tc.venue)
		if err != nil || info.DisplayName != tc.name {
			t.Fatalf("wrong public name: %q %v", info.DisplayName, err)
		}
		slots, err := svc.availability(ctx, tc.venue, "2030-11-20", 2, now)
		if err != nil || len(slots) != 1 || slots[0].StartAt != tc.start {
			t.Fatalf("wrong published slots: %v %v", slots, err)
		}
		result, err := svc.request(ctx, tc.venue, reservationInput{GuestName: "Fictional guest", PartySize: 2, StartAt: tc.start}, now)
		if err != nil || result.Status != "pending" {
			t.Fatalf("bound reservation failed: %v %v", result, err)
		}
		var outlet int64
		if err := db.QueryRowContext(ctx, "SELECT outlet_id FROM reservation_requests WHERE id=$1", result.ID).Scan(&outlet); err != nil || outlet != tc.outlet {
			t.Fatalf("request crossed outlet boundary: %d %v", outlet, err)
		}
	}
	const north = "fiction-bakery-north"
	northScope := scope(1, 1, north)
	_, err := northScope.info(ctx, "fiction-bakery-west")
	expectUnavailable(err)
	_, err = northScope.availability(ctx, "fiction-cafe-east", "2030-11-20", 2, now)
	expectUnavailable(err)
	_, err = scope(2, 4, "fiction-cafe-south").info(ctx, "fiction-cafe-south")
	expectUnavailable(err)
	_, err = scope(2, 1, north).info(ctx, north)
	expectUnavailable(err)
	_, err = publishedReservations(db, reservationPublication{outlet: 1, venue: north})
	expectUnavailable(err)
	_, err = northScope.request(ctx, north, reservationInput{"Fictional guest", 2, "2030-11-20T16:30:00Z"}, now)
	if !errors.Is(err, reservationConflict) {
		t.Fatalf("a different branch's time did not fail closed: %v", err)
	}

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if err := lockPostgresOutlet(ctx, blocker, 1); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := blocker.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := northScope.request(ctx, north, reservationInput{"Fictional waiter", 2, "2030-11-20T15:00:00Z"}, now)
		done <- err
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`,
			pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("request did not reach outlet lock: %v", err)
		case <-deadline.C:
			t.Fatal("request never waited on the original outlet")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE reservation_venues SET slug='fiction-bakery-new' WHERE outlet_id=1;
		UPDATE reservation_venues SET slug='fiction-bakery-north' WHERE outlet_id=3`); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		expectUnavailable(err)
	case <-deadline.C:
		t.Fatal("request did not finish after outlet change")
	}
	_, err = northScope.info(ctx, north)
	expectUnavailable(err)
	_, err = northScope.availability(ctx, north, "2030-11-20", 2, now)
	expectUnavailable(err)
	unbound := reservationsService{db: db}
	if current, err := unbound.info(ctx, north); err != nil || current.DisplayName != "Cafe East" {
		t.Fatalf("existing public venue lookup changed: %q %v", current.DisplayName, err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM reservation_requests").Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("denied requests changed reservations: %d %v", rows, err)
	}
}
