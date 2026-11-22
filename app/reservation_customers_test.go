package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyaruka/phonenumbers/v2"
)

func TestNormalizeReservationPhone(t *testing.T) {
	tests := map[string]string{
		"+54 9 11 2345-6789":    "+5491123456789",
		"+54 (11) 2345.6789":    "+541123456789",
		"+1 (202) 555-0100":     "+12025550100",
		"011 15 2345-6789":      "",
		"+54 011 15 2345-6789":  "",
		"00 44 7700 900123":     "",
		"+999 123456":           "",
		"+1 202 555 0100 ext 6": "",
		"+1 202\n5550100":       "",
		"+１2025550100":          "",
	}
	uk := phonenumbers.GetExampleNumberForTypeInRegion("GB", phonenumbers.MOBILE)
	tests[phonenumbers.Format(uk, phonenumbers.INTERNATIONAL)] = phonenumbers.Format(uk, phonenumbers.E164)
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, ok := normalizeReservationPhone(input)
			if ok != (want != "") || (ok && got != want) {
				t.Fatal("unexpected validation or normalization", got, ok)
			}
		})
	}
}

func TestPostgresReservationCustomerContacts(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgresThrough(ctx, db, postgresMigrationNames[:len(postgresMigrationNames)-1]); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(7701,'Fixture org','00000007701',TRUE),(7702,'Other org','00000007702',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(7701,7701,'First',TRUE),(7702,7701,'Second',TRUE),(7703,7702,'Other',TRUE);
 INSERT INTO reservation_venues VALUES(7701,'fixture-one','First','',TRUE),(7702,'fixture-two','Second','',TRUE),(7703,'fixture-other','Other','',TRUE);
 INSERT INTO reservation_tables VALUES(7701,'A',4),(7702,'B',4),(7703,'C',4);
 INSERT INTO reservation_slots VALUES(7701,'2030-11-20T15:00:00Z'),(7702,'2030-11-20T15:00:00Z'),(7703,'2030-11-20T15:00:00Z');
 INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,created_at)
 VALUES('00000000000000000000000000000001',7701,'2030-11-20T15:00:00Z','legacy fixture',2,'pending','2030-11-18T10:00:00Z');
 `)
	if err != nil {
		t.Fatal(err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	var gates, legacy int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM reservation_venues WHERE collect_contact),(SELECT count(*) FROM reservation_requests WHERE guest_name='legacy fixture')`).Scan(&gates, &legacy); err != nil || gates != 0 || legacy != 1 {
		t.Fatal(gates, legacy, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE reservation_venues SET collect_contact=TRUE WHERE outlet_id IN (7701,7702,7703)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 11, 19, 12, 0, 0, 0, time.UTC)
	a := &app{db: db, reservationContacts: true}
	mux := http.NewServeMux()
	a.registerReservationPublic(func(path, method string, h http.HandlerFunc) { mux.HandleFunc(method+" "+path, h) }, true, func() time.Time { return now })
	ask := func(venue, method, path string, body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "http://example.com/api/v1/public/reservas/"+venue+path, strings.NewReader(string(raw))).WithContext(ctx)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if response := ask("fixture-one", "GET", "", nil); response.Code != 200 || !strings.Contains(response.Body.String(), `"collect_contact":true`) {
		t.Fatal(response)
	}
	payload := func(name, phone, note string, party int) map[string]any {
		return map[string]any{"guest_name": name, "phone": phone, "note": note, "party_size": party, "start_at": "2030-11-20T15:00:00Z"}
	}
	for _, bad := range []struct {
		body   map[string]any
		status int
	}{
		{payload("First", "", "", 2), 422}, {payload("First", "+5491123456789", strings.Repeat("á", 301), 2), 422},
		{payload("First", "+12025550100", "private-other", 11), 409},
	} {
		res := ask("fixture-one", "POST", "/requests", bad.body)
		if res.Code != bad.status {
			t.Fatalf("invalid call got %d want %d: %s", res.Code, bad.status, res.Body)
		}
	}
	done := make(chan *httptest.ResponseRecorder, 3)
	var wg sync.WaitGroup
	for i, e := range []struct{ venue, phone string }{{"fixture-one", "+54 9 11 2345-6789"}, {"fixture-two", "+5491123456789"}, {"fixture-other", "+5491123456789"}} {
		wg.Go(func() {
			done <- ask(e.venue, "POST", "/requests", payload(fmt.Sprintf("Person %d", i), e.phone, fmt.Sprintf("private-note-%d", i), 2))
		})
	}
	wg.Wait()
	close(done)
	for res := range done {
		if res.Code != 201 {
			t.Fatal(res.Code, res.Body)
		}
		wire := res.Body.String()
		for _, bad := range []string{"phone", "+549", "private-note", "Person", "customer", "entity"} {
			if strings.Contains(wire, bad) {
				t.Fatal("public result disclosed private data")
			}
		}
		if !strings.Contains(wire, `"status":"pending"`) {
			t.Fatal(wire)
		}
	}
	var customers, contacts, linked, requestNames, distinctNotes int
	err = db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM reservation_customers),(SELECT count(*) FROM reservation_private_contacts),
 (SELECT count(DISTINCT customer_id) FROM reservation_private_contacts WHERE entity_id=7701),
 (SELECT count(DISTINCT guest_name) FROM reservation_requests WHERE guest_name LIKE 'Person %'),
 (SELECT count(DISTINCT note) FROM reservation_private_contacts)`).Scan(&customers, &contacts, &linked, &requestNames, &distinctNotes)
	if err != nil || customers != 2 || contacts != 3 || linked != 1 || requestNames != 3 || distinctNotes != 3 {
		t.Fatal(customers, contacts, linked, requestNames, distinctNotes, err)
	}
	_, err = db.ExecContext(ctx, `UPDATE reservation_private_contacts SET customer_id=(SELECT id FROM reservation_customers WHERE entity_id=7702) WHERE outlet_id=7701`)
	if reservationConstraintCode(err) != "23503" {
		t.Fatal("cross-entity customer was not rejected", err)
	}
	a.reservationContacts = false
	if response := ask("fixture-one", "GET", "", nil); response.Code != 200 || strings.Contains(response.Body.String(), `"collect_contact"`) {
		t.Fatal(response)
	}
	if response := ask("fixture-one", "POST", "/requests", payload("Flag disabled", "+12025550100", "not stored", 2)); response.Code != 422 {
		t.Fatal(response.Code, response.Body)
	}
}

func TestPostgresContactSettingsRejectsConcurrentGrantRevocation(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const user = "contact-authorization-fixture"
	const pass = "synthetic contact authorization password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(7801,$1,$2,TRUE,FALSE,FALSE)`, user, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(7801,'Synthetic contacts','00000007801',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(7801,7801,'Center',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(7801,7801,7801,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(7801,7801);
 INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled) VALUES(7801,'fixture-authz','Center','',TRUE);
 `); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com", publicReservations: true, reservationContacts: true}
	router := a.routes()
	cookies := postgresTestCookies(t, router, user, pass)
	const endpoint = "http://example.com/api/v1/entities/7801/outlets/7801/reservations/contact-settings"
	send := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, endpoint, strings.NewReader(`{"collect_contact":true}`)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = lockPostgresEntity(ctx, tx, 7801); err != nil {
		t.Fatal(err)
	}
	var blocker int
	if err = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM outlet_grants WHERE membership_id=7801 AND outlet_id=7801`); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- send() }()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	waiting := false
	for !waiting {
		select {
		case w := <-done:
			t.Fatalf("write finished before entity lock was released: %d %s", w.Code, w.Body)
		case <-deadline.C:
			t.Fatal("write never waited for the membership-change transaction")
		case <-tick.C:
			if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE a.datname=current_database() AND $1=ANY(pg_blocking_pids(a.pid)))`, blocker).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != http.StatusNotFound {
			t.Fatal(w.Code, w.Body)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var persisted bool
	if err = db.QueryRowContext(ctx, `SELECT collect_contact FROM reservation_venues WHERE outlet_id=7801`).Scan(&persisted); err != nil || persisted {
		t.Fatal(persisted, err)
	}
}
