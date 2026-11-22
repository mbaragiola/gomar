package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresMCPReservationConfirmAdapter(t *testing.T) {
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
		`INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(724001,'Mesa 1',4),(724001,'Mesa 2',6)`,
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
	s := reservationConfirmScope{id, id, id}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	p, err := prepareReservationConfirm(ctx, tx, s, reservationConfirmInput{ids[0]}, now)
	if err != nil || p.PartySize != 4 || p.TableLabel != "Mesa 1" || p.TableCapacity != 4 || p.StartLocal != start.Format(time.RFC3339) || p.Status != "pending" {
		t.Fatal(p, err)
	}
	second, err := prepareReservationConfirm(ctx, tx, s, reservationConfirmInput{ids[1]}, now)
	if err != nil || second.TableLabel != "Mesa 1" {
		t.Fatal(second, err)
	}
	if _, err := prepareReservationConfirm(ctx, tx, reservationConfirmScope{id, id, id + 1}, reservationConfirmInput{ids[0]}, now); err != missingOutlet {
		t.Fatal(err)
	}
	if _, err := prepareReservationConfirm(ctx, tx, s, reservationConfirmInput{ids[0]}, start); err != reservationStateConflict {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(p)
	if strings.Contains(string(serialized), "Persona Uno") || strings.Contains(string(serialized), "Avisá") {
		t.Fatal("preview includes guest or policy")
	}
	check := func(want int) {
		t.Helper()
		var confirmed, pending, policies, otherTable int
		err := db.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE status='confirmed'),count(*) FILTER(WHERE status='pending'),
 (SELECT count(*) FROM reservation_request_policies WHERE policy='Avisá si no venís.'),count(*) FILTER(WHERE table_label='Mesa 2')
 FROM reservation_requests WHERE outlet_id=$1`, id).Scan(&confirmed, &pending, &policies, &otherTable)
		if err != nil || confirmed != want || pending != 2-want || policies != 2 || otherTable != 0 {
			t.Fatal(confirmed, pending, policies, otherTable, err)
		}
	}
	apply := func(preview reservationConfirmPreview, digest [32]byte, authenticated, commit bool) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		req := r
		if !authenticated {
			req = httptest.NewRequestWithContext(ctx, "POST", "http://example.com/", nil)
		}
		result, err := applyReservationConfirm(req, tx, user{id: id}, s, preview, digest, func() time.Time { return now })
		if err != nil {
			return err
		}
		if result.TableLabel != "Mesa 1" || result.Status != "confirmed" {
			t.Errorf("unexpected result: %+v", result)
		}
		if commit {
			return tx.Commit()
		}
		return nil
	}
	check(0)
	if err := apply(p, p.digest(), false, true); err != unauthenticated {
		t.Fatal(err)
	}
	for _, role := range []string{"purchasing", "administration"} {
		if _, err := db.ExecContext(ctx, `UPDATE memberships SET role=$1 WHERE id=$2`, role, id); err != nil {
			t.Fatal(err)
		}
		if role == "purchasing" {
			if err := apply(p, p.digest(), true, true); err != forbiddenRole {
				t.Fatal("expected current role refusal:", err)
			}
		}
	}
	if err := apply(p, p.digest(), true, false); err != nil {
		t.Fatal(err)
	}
	check(0)
	out := make(chan error, 2)
	for _, preview := range []reservationConfirmPreview{p, second} {
		go func() { out <- apply(preview, preview.digest(), true, true) }()
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
	check(1)
}

func TestDecodeReservationConfirm(t *testing.T) {
	const ref = "32f520f29226462cac5603813cad71f1"
	if v, err := decodeReservationConfirm([]byte(`{"reservation_reference":"` + ref + `"}`)); err != nil || v.Reference != ref {
		t.Fatal(v, err)
	}
	for _, raw := range []string{`{"reservation_reference":null}`, `{"reservation_reference":"` + ref + `","table_label":"Mesa 3"}`, `{"reservation_reference":"` + strings.ToUpper(ref) + `"}`} {
		if _, err := decodeReservationConfirm([]byte(raw)); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
