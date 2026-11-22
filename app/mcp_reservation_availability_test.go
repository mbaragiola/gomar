package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresMCPReservationAvailability(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const username = "availability-qa"
	const password = "synthetic availability fixture password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'availability-qa',$1,TRUE,FALSE,FALSE),(2,'availability-reader',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	const fixture = `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture','00000000001',TRUE),(2,'Foreign','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Center',TRUE),(2,1,'Disabled',TRUE),(3,1,'Missing',TRUE),(4,2,'Foreign',TRUE),(5,1,'Ungranted',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'read_only',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,3),(2,1);
 INSERT INTO reservation_venues VALUES(1,'fixture','Center','private-policy',TRUE),(2,'disabled','Disabled','private-policy',FALSE);
 INSERT INTO reservation_tables VALUES(1,'private-two',2),(1,'private-six',6);
 INSERT INTO reservation_slots VALUES(1,'2030-11-20T03:00:00Z'),(1,'2030-11-20T15:00:00Z'),(1,'2030-11-20T16:30:00Z'),(1,'2030-11-20T18:00:00Z');
 INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,table_label,created_at) VALUES
 ('00000000000000000000000000000001',1,'2030-11-20T15:00:00Z','private-confirmed',6,'confirmed','private-six','2030-11-19T12:00:00Z'),
 ('00000000000000000000000000000002',1,'2030-11-20T16:30:00Z','private-pending',6,'pending',NULL,'2030-11-19T12:00:00Z'),
 ('00000000000000000000000000000003',1,'2030-11-20T18:00:00Z','private-cancelled',6,'cancelled',NULL,'2030-11-19T12:00:00Z');
 `
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 11, 20, 9, 0, 0, 0, reservationLocation)
	a := &app{db: db, host: "example.com", enableMCP: true, publicReservations: true, mcpClock: func() time.Time { return now }}
	mux := a.routes()
	cookies := postgresTestCookies(t, mux, username, password)
	readerCookies := postgresTestCookies(t, mux, "availability-reader", password)
	call := func(h http.Handler, method, path, body, key string, session []*http.Cookie, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		for _, c := range session {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s got %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		if strings.HasPrefix(path, "/api/v1/mcp/") && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return w.Body.String()
	}
	issue := func(scope string, session []*http.Cookie) string {
		t.Helper()
		var r struct{ Token string }
		if err := json.Unmarshal([]byte(call(mux, "POST", "/api/v1/auth/mcp-token", scope, "", session, 201)), &r); err != nil || len(r.Token) != 43 {
			t.Fatal("token unavailable")
		}
		return r.Token
	}
	broad := issue(`{"entity_id":1}`, cookies)
	narrow := issue(`{"entity_id":1,"outlet_id":1}`, cookies)
	reader := issue(`{"entity_id":1,"outlet_id":1}`, readerCookies)
	const prefix = "/api/v1/mcp/entities/1/outlets/"
	const fixed = "/reservation-availability?date=2030-11-20&party_size="
	good := call(mux, "GET", prefix+"1"+fixed+"6", "", narrow, nil, 200)
	if good != "{\"date\":\"2030-11-20\",\"timezone\":\"America/Argentina/Buenos_Aires\",\"party_size\":6,\"slots\":[{\"start_at\":\"2030-11-20T13:30:00-03:00\"},{\"start_at\":\"2030-11-20T15:00:00-03:00\"}]}\n" {
		t.Fatal(good)
	}
	if got := call(mux, "GET", prefix+"1"+fixed+"2", "", narrow, nil, 200); !strings.Contains(got, "2030-11-20T12:00:00-03:00") {
		t.Fatal(got)
	}
	for _, url := range []string{prefix + "1" + fixed + "12", prefix + "1/reservation-availability?date=" + now.AddDate(0, 0, 60).Format(time.DateOnly) + "&party_size=1"} {
		if got := call(mux, "GET", url, "", narrow, nil, 200); !strings.Contains(got, `"slots":[]`) {
			t.Fatal(got)
		}
	}
	for _, url := range []string{prefix + "1" + fixed + "0", prefix + "1" + fixed + "13", prefix + "1" + fixed + "6&x=1", prefix + "1" + fixed + "6&party_size=7",
		prefix + "1/reservation-availability?date=2030-11-19&party_size=1", prefix + "1/reservation-availability?date=" + now.AddDate(0, 0, 61).Format(time.DateOnly) + "&party_size=1"} {
		call(mux, "GET", url, "", narrow, nil, 422)
	}
	for _, test := range []struct {
		path, key string
		code      int
	}{
		{prefix + "2" + fixed + "6", broad, 404}, {prefix + "3" + fixed + "6", broad, 404}, {prefix + "2" + fixed + "6", narrow, 404},
		{prefix + "5" + fixed + "6", broad, 404}, {"/api/v1/mcp/entities/2/outlets/4" + fixed + "6", broad, 404},
		{prefix + "1" + fixed + "6", reader, 403}, {prefix + "1" + fixed + "6", "", 401},
	} {
		call(mux, "GET", test.path, "", test.key, nil, test.code)
	}
	a.publicReservations = false
	call(a.routes(), "GET", prefix+"1"+fixed+"6", "", narrow, nil, 404)
	native := http.NewServeMux()
	a.registerReservationPublic(func(path, method string, h http.HandlerFunc) { native.HandleFunc(method+" "+path, h) }, true, func() time.Time { return now })
	if got := call(native, "GET", "/api/v1/public/reservas/fixture/availability?date=2030-11-20&party_size=6", "", "", nil, 200); got != "{\"slots\":[{\"start_at\":\"2030-11-20T16:30:00Z\"},{\"start_at\":\"2030-11-20T18:00:00Z\"}]}\n" {
		t.Fatal(got)
	}
}
