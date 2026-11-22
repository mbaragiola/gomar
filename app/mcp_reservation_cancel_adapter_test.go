package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresMCPReservationCancelAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const id = 724001
	const password = "synthetic reservation test password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(724001,'reservation-test-pg',$1,true,false,false)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO entities(id,name,cuit,active) VALUES(724001,'Synthetic reservation entity','00000724001',true)`,
		`INSERT INTO outlets(id,entity_id,name,active) VALUES(724001,724001,'Synthetic reservation outlet',true)`,
		`INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(724001,724001,724001,'administration',true)`,
		`INSERT INTO outlet_grants(membership_id,outlet_id) VALUES(724001,724001)`,
		`INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled)
     VALUES(724001,'reserva-qa-pg','Synthetic reservation outlet','Avisá si no venís.',true)`,
		`INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(724001,'Mesa 1',4)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	local := now.In(reservationLocation).AddDate(0, 0, 1)
	start := time.Date(local.Year(), local.Month(), local.Day(), 21, 0, 0, 0, reservationLocation)
	if _, err := db.ExecContext(ctx, `INSERT INTO reservation_slots(outlet_id,start_at) VALUES($1,$2)`, id, start.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	svc := reservationsService{db: db}
	slots, err := svc.availability(ctx, "reserva-qa-pg", start.Format(time.DateOnly), 4, now)
	if err != nil || len(slots) != 1 || slots[0].StartAt != start.UTC().Format(time.RFC3339) {
		t.Fatal(slots, err)
	}
	ids := []string{}
	for _, guest := range []string{"Persona Uno", "Persona Dos"} {
		item, err := svc.request(ctx, "reserva-qa-pg", reservationInput{GuestName: guest, PartySize: 4, StartAt: start.Format(time.RFC3339)}, now)
		if err != nil || item.Status != "pending" || item.CancellationPolicy == nil || *item.CancellationPolicy != "Avisá si no venís." {
			t.Fatal(item, err)
		}
		ids = append(ids, item.ID)
	}
	a := &app{db: db, host: "example.com", publicReservations: true}
	handler := a.routes()
	cookies := postgresTestCookies(t, handler, "reservation-test-pg", password)
	r := httptest.NewRequestWithContext(ctx, "POST", "http://example.com/", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	scope := reservationCancelScope{id, id, id}
	prepare := func(i int, when time.Time) (reservationCancelPreview, error) {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return reservationCancelPreview{}, err
		}
		defer tx.Rollback()
		return prepareReservationCancel(ctx, tx, scope, reservationCancelInput{ids[i]}, when)
	}
	pending, err := prepare(0, now)
	if err != nil || pending.Status != "pending" || pending.TableLabel != "" || pending.PartySize != 4 || pending.StartLocal != start.Format(time.RFC3339) {
		t.Fatal(pending, err)
	}
	if _, err = prepare(0, start); err != reservationStateConflict {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = lockPostgresOutlet(ctx, tx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = reservationChange(ctx, tx, id, ids[0], "confirm", now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	confirmed, err := prepare(0, now)
	if err != nil || confirmed.Status != "confirmed" || confirmed.TableLabel != "Mesa 1" {
		t.Fatal(confirmed, err)
	}
	serialized, _ := json.Marshal(confirmed)
	if strings.Contains(string(serialized), "Persona Uno") || strings.Contains(string(serialized), "Avisá") {
		t.Fatal("guest data in preview")
	}
	apply := func(p reservationCancelPreview, authenticated, commit bool, s reservationCancelScope) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		req := r
		if !authenticated {
			req = httptest.NewRequestWithContext(ctx, "POST", "http://example.com/", nil)
		}
		result, err := applyReservationCancel(req, tx, user{id: id}, s, p, p.digest(), func() time.Time { return now })
		if err != nil {
			return err
		}
		if result.Status != "cancelled" || result.TableLabel != "" {
			t.Errorf("unexpected cancellation result: %+v", result)
		}
		if commit {
			return tx.Commit()
		}
		return nil
	}
	if err = apply(pending, true, true, scope); err != reservationStateConflict {
		t.Fatal("old pending snapshot", err)
	}
	if err = apply(confirmed, false, true, scope); err != unauthenticated {
		t.Fatal("unauthenticated", err)
	}
	other := confirmed
	other.Scope.Outlet++
	if err = apply(other, true, true, other.Scope); err != missingOutlet {
		t.Fatal("ungranted outlet", err)
	}
	for _, role := range []string{"purchasing", "administration"} {
		if _, err = db.ExecContext(ctx, `UPDATE memberships SET role=$1 WHERE id=$2`, role, id); err != nil {
			t.Fatal(err)
		}
		if role == "purchasing" {
			if err = apply(confirmed, true, true, scope); err != forbiddenRole {
				t.Fatal("role downgrade", err)
			}
		}
	}
	if err = apply(confirmed, true, false, scope); err != nil {
		t.Fatal(err)
	}
	if p, err := prepare(0, now); err != nil || p != confirmed {
		t.Fatal(p, err)
	}
	out := make(chan error, 2)
	for range 2 {
		go func() { out <- apply(confirmed, true, true, scope) }()
	}
	accepted, stale := 0, 0
	for range 2 {
		switch err := <-out; err {
		case nil:
			accepted++
		case reservationStateConflict:
			stale++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || stale != 1 {
		t.Fatal(accepted, stale)
	}
	var status string
	var released bool
	var untouched int
	err = db.QueryRowContext(ctx, `SELECT status,table_label IS NULL,
 (SELECT count(*) FROM reservation_request_policies WHERE policy='Avisá si no venís.')
 FROM reservation_requests WHERE id=$1`, ids[0]).Scan(&status, &released, &untouched)
	if err != nil || status != "cancelled" || !released || untouched != 2 {
		t.Fatal(status, released, untouched, err)
	}
	if p, err := prepare(1, now); err != nil || p.Status != "pending" || p.TableLabel != "" {
		t.Fatal("other request changed", p, err)
	}
}

func TestDecodeReservationCancel(t *testing.T) {
	const ref = "32f520f29226462cac5603813cad71f1"
	if v, err := decodeReservationCancel([]byte(`{"reservation_reference":"` + ref + `"}`)); err != nil || v.Reference != ref {
		t.Fatal(v, err)
	}
	for _, raw := range []string{`{"reservation_reference":null}`, `{"reservation_reference":"` + ref + `","status":"confirmed"}`, `{"reservation_reference":"` + strings.ToUpper(ref) + `"}`} {
		if _, err := decodeReservationCancel([]byte(raw)); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
