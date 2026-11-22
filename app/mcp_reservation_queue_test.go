package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func TestPostgresMCPReservationQueueExactStaffScopeAndProjection(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic reservation queue credential"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'reservation-queue-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(pass)); err != nil {
		t.Fatal(err)
	}
	const fixture = `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Other','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',TRUE),(2,1,'Norte',TRUE),(3,2,'Centro',TRUE),(4,1,'Sin acceso',TRUE),(5,1,'Inactiva',FALSE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,5);
 INSERT INTO reservation_venues VALUES(1,'venue-one','Centro','private-policy',TRUE),(2,'venue-two','Norte','private-policy',TRUE);
 INSERT INTO reservation_tables VALUES(1,'private-table-label',8);
 INSERT INTO reservation_slots VALUES(1,'2030-11-20T02:59:59Z'),(1,'2030-11-20T03:00:00.001Z'),(1,'2030-11-20T12:00:00Z'),
    (1,'2030-11-21T03:00:00.001Z'),(1,'2030-11-20T15:00:00Z'),(2,'2030-11-20T12:00:00Z');
 INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,table_label,created_at) VALUES
 ('00000000000000000000000000000001',1,'2030-11-20T02:59:59Z','private-previous-day',6,'pending',NULL,'2030-11-01T12:00:00Z'),
 ('00000000000000000000000000000002',1,'2030-11-20T03:00:00.001Z','private-first-guest',2,'pending',NULL,'2030-11-01T12:00:00Z'),
 ('00000000000000000000000000000003',1,'2030-11-20T12:00:00Z','private-second-guest',3,'confirmed','private-table-label','2030-11-01T12:00:00Z'),
 ('00000000000000000000000000000004',1,'2030-11-21T03:00:00.001Z','private-next-day',5,'pending',NULL,'2030-11-01T12:00:00Z'),
 ('00000000000000000000000000000005',2,'2030-11-20T12:00:00Z','private-other-outlet',12,'cancelled',NULL,'2030-11-01T12:00:00Z');
 INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,created_at)
 SELECT lpad(to_hex(i),32,'0'),1,'2030-11-20T15:00:00Z','private-guest-list',1,'cancelled','2030-11-01T12:00:00Z'
 FROM generate_series(100,129) i;
 `
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	// Staff can triage existing requests when public intake is off.
	a := app{db: db, host: "example.com", enableMCP: true, publicReservations: false}
	mux := a.routes()
	cookies := postgresTestCookies(t, mux, "reservation-queue-fixture", pass)
	ask := func(method, path, body, key string, login bool, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if login {
			for _, c := range cookies {
				req.AddCookie(c)
				if c.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: %d, want %d", method, path, w.Code, want)
		}
		if strings.HasPrefix(path, "/api/v1/mcp/") && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return w.Body.String()
	}
	issue := func(body string) string {
		t.Helper()
		var k struct{ Token string }
		if json.Unmarshal([]byte(ask("POST", "/api/v1/auth/mcp-token", body, "", true, 201)), &k) != nil || len(k.Token) != 43 {
			t.Fatal("test key unavailable")
		}
		return k.Token
	}
	broad := issue(`{"entity_id":1}`)
	narrow := issue(`{"entity_id":1,"outlet_id":1}`)
	const base = "/api/v1/mcp/entities/1/outlets/1/reservation-requests"
	const day = "date=2030-11-20"
	const first = "00000000000000000000000000000002"
	const second = "00000000000000000000000000000003"
	const tomorrow = "00000000000000000000000000000004"
	const elsewhere = "00000000000000000000000000000005"
	read := func(path, key string, want int, dst any) {
		t.Helper()
		body := ask("GET", base+path, "", key, false, want)
		if dst != nil && json.Unmarshal([]byte(body), dst) != nil {
			t.Fatal("invalid result shape")
		}
		for _, s := range []string{"private-", "guest_name", "contact", "phone", "email", "table_label", "policy", "payment", "notes", "created_at"} {
			if strings.Contains(body, s) {
				t.Fatalf("unexpected private field %s", s)
			}
		}
	}
	ask("GET", base+"?"+day, "", "", true, 401)
	var page mcpclient.ReservationRequests
	read("?"+day, narrow, 200, &page)
	if len(page.Requests) != 30 || page.NextID != "0000000000000000000000000000007f" || page.Requests[0].ID != first ||
		page.Requests[0].StartAt != "2030-11-20T00:00:00.001-03:00" || page.Requests[1].ID != second {
		t.Fatal("wrong local date, order or first page")
	}
	after := page.NextID
	page = mcpclient.ReservationRequests{}
	read("?"+day+"&after_id="+after, narrow, 200, &page)
	if len(page.Requests) != 2 || page.NextID != "" || page.Requests[1].ID != "00000000000000000000000000000081" {
		t.Fatal("wrong last page")
	}
	var entry mcpclient.ReservationRequest
	read("/"+second+"?"+day, narrow, 200, &entry)
	if entry.ID != second || entry.PartySize != 3 || entry.Status != "confirmed" || entry.StartAt != "2030-11-20T09:00:00-03:00" {
		t.Fatal("wrong exact projection")
	}
	for _, id := range []string{tomorrow, elsewhere, "not-a-reference"} {
		read("/"+id+"?"+day, broad, 404, nil)
	}
	for _, query := range []string{"?" + day + "&after_id=" + elsewhere, "?" + day + "&after_id=" + tomorrow, "?" + day + "&after_id=", "?" + day + "&" + day,
		"?date=2030-02-30", "?" + day + "&include=guest", "?after_id=" + second} {
		read(query, broad, 422, nil)
	}
	read("/"+second+"?"+day+"&after_id="+second, narrow, 422, nil)
	ask("GET", "/api/v1/mcp/entities/1/outlets/2/reservation-requests?"+day, "", narrow, false, 404)
	ask("GET", "/api/v1/mcp/entities/1/outlets/4/reservation-requests?"+day, "", broad, false, 404)
	ask("GET", "/api/v1/mcp/entities/1/outlets/5/reservation-requests?"+day, "", broad, false, 404)
	ask("GET", "/api/v1/mcp/entities/2/outlets/3/reservation-requests?"+day, "", broad, false, 404)
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='read_only' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	read("?"+day, narrow, 403, nil)
	read("/"+second+"?"+day, narrow, 403, nil)
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='administration' WHERE id=1;DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1`); err != nil {
		t.Fatal(err)
	}
	read("?"+day, narrow, 404, nil)
}
