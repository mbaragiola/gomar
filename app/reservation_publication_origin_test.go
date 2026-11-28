package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresReservationPublicationOrigin(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	seedPublishedReservationScope(t, ctx, db)
	_, err := db.ExecContext(ctx, `INSERT INTO reservation_public_businesses VALUES
		('fixture-bakery',1,'Fictional public bakery'),('fixture-cafe',2,'Fictional public cafe');
		INSERT INTO reservation_public_branches VALUES
		('fixture-bakery','centro',1,1,'fiction-bakery-north',1,true),
		('fixture-bakery','oeste',1,2,'fiction-bakery-west',3,true),
		('fixture-cafe','centro',2,3,'fiction-cafe-east',1,true),
		('fixture-cafe','sur',2,4,'fiction-cafe-south',1,false)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE reservation_public_branches SET entity_id=2, outlet_id=3,revision=9
		WHERE business_slug='fixture-bakery' AND slug='centro'`); err == nil {
		t.Fatal("publication scope could be rebound")
	}
	now := time.Date(2030, 11, 19, 12, 0, 0, 0, time.UTC)
	secret := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	handler, err := reservationPublicationOrigin(db, secret, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	const base = reservationPublicationOriginPrefix
	callWithKey := func(method, path, credential, rev, body, bookingKey string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://fixture.invalid"+base+path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+credential)
		if rev != "" {
			r.Header.Set("If-Match", rev)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", bookingKey)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	call := func(method, path, credential, rev, body string) *httptest.ResponseRecorder {
		return callWithKey(method, path, credential, rev, body, "0112233445566778899aabbccddeeff0")
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("got %d want %d\n%s", w.Code, status, w.Body.String())
		}
	}
	assertStatus(call(http.MethodGet, "fixture-bakery", "not-the-service", "", ""), 404)
	business := call(http.MethodGet, "fixture-bakery", secret, "", "")
	assertStatus(business, 200)
	var payload publishedReservationBusiness
	if err := json.Unmarshal(business.Body.Bytes(), &payload); err != nil || len(payload.Branches) != 2 ||
		payload.DisplayName != "Fictional public bakery" || payload.Branches[0].DisplayName != "Bakery North" ||
		strings.Contains(business.Body.String(), "North internal") {
		t.Fatalf("wrong public business data: %s %v", business.Body.String(), err)
	}
	assertStatus(call(http.MethodGet, "unknown", secret, "", ""), 404)
	assertStatus(call(http.MethodGet, "fixture-cafe/sur", secret, "", ""), 404)
	assertStatus(call(http.MethodGet, "fixture-bakery/centro/staff", secret, "", ""), 404)
	cafe := call(http.MethodGet, "fixture-cafe/centro", secret, "", "")
	assertStatus(cafe, 200)
	if !strings.Contains(cafe.Body.String(), "Cafe East") {
		t.Fatalf("the branch alias resolved outside its business: %s", cafe.Body.String())
	}
	path := "fixture-bakery/centro/requests"
	body := `{"guest_name":"Fictional guest","party_size":2,"start_at":"2030-11-20T15:00:00Z"}`
	assertStatus(call(http.MethodPost, path, secret, `"3"`, body), 412)
	assertStatus(call(http.MethodPost, "fixture-cafe/centro/requests", secret, `"1"`, body), 409)
	assertStatus(call(http.MethodPost, path, secret, `"1"`, body), 201)
	first := call(http.MethodPost, path, secret, `"1"`, body)
	assertStatus(first, 201)
	again := call(http.MethodPost, path, secret, `"1"`, body)
	assertStatus(again, 201)
	if first.Body.String() != again.Body.String() {
		t.Fatal("an exact retry changed the booking receipt")
	}
	assertStatus(call(http.MethodPost, path, secret, `"1"`, strings.Replace(body, "Fictional guest", "Changed guest", 1)), 409)

	before := now
	now = now.Add(48 * time.Hour)
	late := call(http.MethodPost, path, secret, `"1"`, body)
	assertStatus(late, 201)
	if late.Body.String() != first.Body.String() {
		t.Fatal("a retry after slot start changed the booking receipt")
	}
	assertStatus(callWithKey(http.MethodPost, path, secret, `"1"`, body, strings.Repeat("a", 32)), 422)
	now = before

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
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(http.MethodPost, path, secret, `"1"`, body) }()
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
		case response := <-done:
			t.Fatalf("booking returned before it reached the lock: %d", response.Code)
		case <-deadline.C:
			t.Fatal("booking did not wait at the outlet")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE reservation_public_branches SET enabled=false,revision=2
		WHERE business_slug='fixture-bakery' AND slug='centro'`); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-done:
		assertStatus(response, 412)
	case <-deadline.C:
		t.Fatal("booking did not return after withdrawal")
	}
	assertStatus(call(http.MethodGet, "fixture-bakery/centro", secret, "", ""), 404)
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM reservation_requests").Scan(&total); err != nil || total != 1 {
		t.Fatalf("denials changed bookings: %d %v", total, err)
	}
}
