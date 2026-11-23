package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func TestPostgresMCPBookingReadsOmitPrivateDataAndRespectFlags(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pwd = "synthetic booking report pass"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'booking-qa',$1,TRUE,FALSE,FALSE)`, encodePassword(pwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic Area','00000000001',TRUE),(2,'Other Entity','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',TRUE),(2,1,'Norte',TRUE),(3,2,'Centro',TRUE),(4,1,'Unconfigured',TRUE),(5,1,'No grant',TRUE),(6,1,'Configured with no menu',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,4),(1,6);
 INSERT INTO reservation_venues VALUES(1,'venue-one','Centro','private-policy',TRUE),(2,'venue-two','Norte','private-policy',FALSE),(6,'venue-six','Configured with no menu','',FALSE);
 INSERT INTO reservation_tables VALUES(1,'private-table-label',8);
 INSERT INTO reservation_slots VALUES(1,'2030-11-20T02:59:59Z'),(1,'2030-11-20T03:00:00.001Z'),
 (1,'2030-11-20T12:00:00Z'),(1,'2030-11-21T03:00:00.100Z'),(2,'2030-11-20T12:00:00Z');
 INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,table_label,created_at) VALUES
 ('00000000000000000000000000000001',1,'2030-11-20T02:59:59Z','Excluded guest',6,'pending',NULL,'2030-11-01T12:00:00Z'),
 ('00000000000000000000000000000002',1,'2030-11-20T03:00:00.001Z','private-name-one',2,'pending',NULL,'2030-11-01T12:00:00Z'),
 ('00000000000000000000000000000003',1,'2030-11-20T12:00:00Z','private-name-two',3,'confirmed','private-table-label','2030-11-01T12:00:00Z'),
 ('00000000000000000000000000000004',1,'2030-11-21T03:00:00.100Z','Excluded at day end',5,'pending',NULL,'2030-11-01T12:00:00Z'),
 ('00000000000000000000000000000005',2,'2030-11-20T12:00:00Z','Excluded outlet',12,'cancelled',NULL,'2030-11-01T12:00:00Z');
 INSERT INTO reservation_menus VALUES(1,convert_to('SYNTHETIC PDF PAYLOAD','UTF8'),'2030-11-20T03:00:00Z',1),
 (2,convert_to('HIDDEN PDF PAYLOAD','UTF8'),'2030-11-20T03:00:00Z',1);
 INSERT INTO pos_products(id,outlet_id,current_revision) SELECT i,1,1 FROM generate_series(1,31) AS i;
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES (99,2,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
 SELECT i,1,1,'Synthetic product '||i,100+i,TRUE FROM generate_series(1,31) AS i;
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
 VALUES (99,2,1,'Unselected product',9000,TRUE);
 INSERT INTO stock_outlets VALUES(1,TRUE),(2,TRUE);
 INSERT INTO stock_products VALUES(1,1,TRUE,1),(2,99,TRUE,1);
 INSERT INTO stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id) VALUES
 (1,1,1,'recepcion',9,9,'2030-11-20T02:59:59.999Z','11111111-0000-4000-8000-000000000001'),
 (1,1,1,'recepcion',5,5,'2030-11-20T03:00:00.001Z','11111111-0000-4000-8000-000000000002'),
 (1,1,1,'merma',2,-2,'2030-11-20T12:00:00Z','11111111-0000-4000-8000-000000000003'),
 (1,1,1,'recepcion',8,8,'2030-11-21T03:00:00.001Z','11111111-0000-4000-8000-000000000004');
 `); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com", enableMCP: true, publicReservations: true, enablePOS: true, enableStock: true,
		mcpClock: func() time.Time { return time.Date(2030, 11, 20, 12, 0, 0, 0, time.UTC) }}
	routes := a.routes()
	cookies := postgresTestCookies(t, routes, "booking-qa", pwd)
	invoke := func(mux http.Handler, method, path, data, token string, auth bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(data)).WithContext(ctx)
		if data != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if auth {
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s returned %d, want %d", method, path, w.Code, want)
		}
		return w
	}
	decode := func(r *httptest.ResponseRecorder, out any) {
		t.Helper()
		if err := json.Unmarshal(r.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var c struct{ Token string }
	issue := func(body string) string {
		t.Helper()
		decode(invoke(routes, "POST", "/api/v1/auth/mcp-token", body, "", true, 201), &c)
		if len(c.Token) != 43 {
			t.Fatal("missing test credential")
		}
		return c.Token
	}
	token := issue(`{"entity_id":1,"outlet_id":1}`)
	wide := issue(`{"entity_id":1}`)
	const p = "/api/v1/mcp/entities/1/outlets/1"
	const day = "/reservation-status?date=2030-11-20"
	const movements = "/stock/products/1/movements-summary?days=1&start_date=2030-11-20"
	check := func(path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := invoke(routes, "GET", p+path, "", token, false, want)
		if r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("stored a scoped report")
		}
		return r
	}
	report := check(day, 200)
	var results mcpclient.ReservationStatus
	decode(report, &results)
	if results.Date != "2030-11-20" || len(results.Counts) != 3 || results.Counts[0].RequestCount != 1 ||
		results.Counts[0].PartySizeTotal != 2 || results.Counts[1].PartySizeTotal != 3 || results.Counts[2].RequestCount != 0 {
		t.Fatal("incorrect scoped Buenos Aires day counts")
	}
	if strings.Contains(report.Body.String(), "private-") || strings.Contains(report.Body.String(), "00000000000000000000000000000002") {
		t.Fatal("guest or reservation details escaped")
	}
	var menu mcpclient.PublishedMenu
	publication := check("/published-menu", 200)
	decode(publication, &menu)
	if !menu.Published || menu.Filename != "menu.pdf" || menu.SizeBytes < 16 ||
		strings.Contains(publication.Body.String(), "PAYLOAD") || strings.Contains(publication.Body.String(), "/api") {
		t.Fatal("unexpected public menu metadata")
	}
	hidden := invoke(routes, "GET", "/api/v1/mcp/entities/1/outlets/2/published-menu", "", wide, false, 200)
	if strings.TrimSpace(hidden.Body.String()) != `{"published":false}` {
		t.Fatal("hidden menu metadata escaped")
	}
	noFlags := (&app{db: db, host: "example.com", enableMCP: true, mcpClock: a.mcpClock}).routes()
	unpublished := invoke(noFlags, "GET", p+"/published-menu", "", token, false, 200)
	if strings.TrimSpace(unpublished.Body.String()) != `{"published":false}` {
		t.Fatal("disabled publishing escaped")
	}
	invoke(noFlags, "GET", p+"/pos-catalog", "", token, false, 404)
	invoke(noFlags, "GET", p+movements, "", token, false, 404)
	const setup = "/reservation-setup"
	const zone = "America/Argentina/Buenos_Aires"
	saved, unsaved := true, false
	for _, tc := range []struct {
		outlet string
		want   mcpclient.ReservationSetup
	}{
		{"1", mcpclient.ReservationSetup{VenueConfigured: true, VenueEnabled: true, PublicFeatureEnabled: true, MenuSaved: &saved, Timezone: zone}},
		{"2", mcpclient.ReservationSetup{VenueConfigured: true, VenueEnabled: false, PublicFeatureEnabled: true, MenuSaved: &saved, Timezone: zone}},
		{"4", mcpclient.ReservationSetup{VenueConfigured: false, VenueEnabled: false, PublicFeatureEnabled: true, MenuSaved: &unsaved, Timezone: zone}},
		{"6", mcpclient.ReservationSetup{VenueConfigured: true, VenueEnabled: false, PublicFeatureEnabled: true, MenuSaved: &unsaved, Timezone: zone}},
	} {
		row := invoke(routes, "GET", "/api/v1/mcp/entities/1/outlets/"+tc.outlet+setup, "", wide, false, 200)
		var seen map[string]json.RawMessage
		decode(row, &seen)
		if len(seen) != 5 || seen["menu_saved"] == nil || row.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("unexpected setup fields")
		}
		var got mcpclient.ReservationSetup
		decode(row, &got)
		if !reflect.DeepEqual(got, tc.want) || strings.Contains(row.Body.String(), "private") || strings.Contains(row.Body.String(), "venue-one") ||
			strings.Contains(row.Body.String(), "PAYLOAD") || strings.Contains(row.Body.String(), "2030-11-20") {
			t.Fatal(got)
		}
		native := invoke(routes, "GET", "/api/v1/entities/1/outlets/"+tc.outlet+"/reservations/settings", "", "", true, map[bool]int{true: 200, false: 404}[tc.want.VenueConfigured])
		if tc.want.VenueConfigured {
			var config reservationSettings
			decode(native, &config)
			if config.Enabled != got.VenueEnabled || config.Timezone != got.Timezone {
				t.Fatal("native settings mismatch")
			}
			side := invoke(routes, "GET", "/api/v1/entities/1/outlets/"+tc.outlet+"/reservations/menu", "", "", true, 200)
			var status reservationMenuStatus
			decode(side, &status)
			if status.Available != *got.MenuSaved {
				t.Fatal("native menu status mismatch")
			}
		}
	}
	var off mcpclient.ReservationSetup
	decode(invoke(noFlags, "GET", p+setup, "", token, false, 200), &off)
	if !reflect.DeepEqual(off, mcpclient.ReservationSetup{VenueConfigured: true, VenueEnabled: true, PublicFeatureEnabled: false, MenuSaved: &saved, Timezone: zone}) {
		t.Fatal(off)
	}
	invoke(routes, "GET", "/api/v1/mcp/entities/1/outlets/5"+setup, "", wide, false, 404)
	check(setup+"?date=2030-11-20", 422)

	var catalog mcpclient.POSCatalog
	decode(check("/pos-catalog", 200), &catalog)
	if catalog.Currency != "ARS" || len(catalog.Products) != 30 || catalog.NextProductID != 30 || catalog.Products[0].PriceMinor != 101 {
		t.Fatal("incorrect first POS catalog page")
	}
	catalog = mcpclient.POSCatalog{}
	decode(check("/pos-catalog?after_product_id=30", 200), &catalog)
	if len(catalog.Products) != 1 || catalog.Products[0].ID != 31 || catalog.NextProductID != 0 {
		t.Fatal("incorrect last POS catalog page")
	}
	var summary mcpclient.StockMovements
	decode(check(movements, 200), &summary)
	if summary.ProductID != 1 || len(summary.Kinds) != 4 || summary.Kinds[0].NetUnits != 5 || summary.Kinds[1].NetUnits != -2 ||
		summary.EndDateExclusive != "2030-11-21" {
		t.Fatal("unexpected grouped stock balances")
	}
	for _, path := range []string{day, setup, "/published-menu", "/pos-catalog", movements} {
		invoke(routes, "GET", "/api/v1/mcp/entities/1/outlets/2"+path, "", token, false, 404)
		invoke(routes, "GET", "/api/v1/mcp/entities/2/outlets/3"+path, "", token, false, 404)
	}
	check("/reservation-status?date=2030-02-30", 422)
	check("/stock/products/1/movements-summary?days=32&start_date=2030-11-20", 422)
	check("/stock/products/99/movements-summary?days=1&start_date=2030-11-20", 404)
	if _, err := db.ExecContext(ctx, `UPDATE memberships SET role='read_only' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	check(day, 403)
	check(setup, 403)
	check("/published-menu", 403)
	check("/pos-catalog", 200)
	check(movements, 403)
}
