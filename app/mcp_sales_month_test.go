package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresMCPNovemberSalesAndOfflineReplay(t *testing.T) {
	ctx, db := testPostgresDB(t)
	var alreadyInitialized bool
	if err := db.QueryRowContext(ctx, "SELECT to_regnamespace('gomar') IS NOT NULL").Scan(&alreadyInitialized); err != nil || alreadyInitialized {
		t.Fatalf("requires a fresh PostgreSQL QA database: initialized=%t, err=%v", alreadyInitialized, err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-November-report-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
		VALUES(1,'november-report-clerk',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO entities (id,name,cuit,active) VALUES
		 (1,'Comedor de prueba','00000000001',TRUE),(2,'Comedor ajeno de prueba','00000000002',TRUE);
		INSERT INTO outlets (id,entity_id,name,active) VALUES
		 (1,1,'Centro',TRUE),(2,1,'Reintento de prueba',TRUE),(3,2,'Centro',TRUE);
		INSERT INTO memberships (id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2);
		INSERT INTO pos_sales (request_id,outlet_id,actor_id,total_minor,recorded_at) VALUES
		 ('60000000-0000-4000-8000-000000000001',1,1,25000,'2026-11-01T03:00:00Z'),
		 ('60000000-0000-4000-8000-000000000002',1,1,30000,'2026-12-01T02:59:59.999999999Z'),
		 ('60000000-0000-4000-8000-000000000003',1,1,11000,'2026-11-01T02:59:59.999999999Z'),
		 ('60000000-0000-4000-8000-000000000004',1,1,12000,'2026-12-01T03:00:00Z'),
		 ('60000000-0000-4000-8000-000000000005',3,1,99000,'2026-11-15T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, time.December, 1, 1, 30, 0, 0, time.UTC)
	a := &app{db: db, host: "example.com", enableMCP: true, enablePOS: true, mcpClock: func() time.Time { return now }}
	handler := a.routes()
	cookies := postgresTestCookies(t, handler, "november-report-clerk", password)
	call := func(method, path, body, token string, session bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if session {
			for _, c := range cookies {
				req.AddCookie(c)
				if c.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != status || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: HTTP %d (want %d), cache %q", method, path, rec.Code, status, rec.Header().Get("Cache-Control"))
		}
		return rec
	}
	issue := func(body string) string {
		t.Helper()
		var reply struct {
			Token string `json:"token"`
		}
		result := call(http.MethodPost, "/api/v1/auth/mcp-token", body, "", true, http.StatusCreated)
		if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil || len(reply.Token) != 43 {
			t.Fatal("did not issue a well-formed synthetic credential")
		}
		return reply.Token
	}
	read := func(token string) mcpSales {
		t.Helper()
		result := call(http.MethodGet, "/api/v1/mcp/entities/1/sales-summary?period=this_month", "", token, false, http.StatusOK)
		var summary mcpSales
		if err := json.Unmarshal(result.Body.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		return summary
	}

	token := issue(`{"entity_id":1,"outlet_id":1}`)
	report := read(token)
	from, fromErr := time.Parse(time.RFC3339, report.From)
	to, toErr := time.Parse(time.RFC3339, report.To)
	if fromErr != nil || toErr != nil || from.UTC().Format(time.RFC3339) != "2026-11-01T03:00:00Z" ||
		to.UTC().Format(time.RFC3339) != "2026-12-01T03:00:00Z" || report.Timezone != "America/Argentina/Buenos_Aires" ||
		report.Currency != "ARS" || report.SaleCount != 2 || report.TotalMinor != 55000 || len(report.Outlets) != 1 ||
		report.Outlets[0].ID != 1 || report.Outlets[0].Name != "Centro" || report.Outlets[0].SaleCount != 2 || report.Outlets[0].TotalMinor != 55000 {
		t.Fatalf("unexpected November summary: %+v", report)
	}
	call(http.MethodGet, "/api/v1/mcp/entities/2/sales-summary?period=this_month&outlet_id=3", "", token, false, http.StatusNotFound)
	call(http.MethodGet, "/api/v1/mcp/entities/1/sales-summary?period=this_month&outlet_id=3", "", token, false, http.StatusNotFound)

	// Exercise the same stable request issued by a reconnecting browser, not
	// browser storage itself. Native POS uses its real ingestion clock.
	const pos = "/api/v1/entities/1/outlets/2/pos"
	call(http.MethodPost, pos+"/catalog", `{"nombre":"Producto de prueba","precio_centavos":25000,"activo":true}`, "", true, http.StatusCreated)
	const queued = `{"id_solicitud":"60000000-0000-4000-8000-000000000006","actor_id":1,"items":[{"producto_id":1,"revision":1,"cantidad":1}],"metodo_pago":"efectivo"}`
	first := call(http.MethodPost, pos+"/sales", queued, "", true, http.StatusCreated)
	replay := call(http.MethodPost, pos+"/sales", queued, "", true, http.StatusOK)
	if first.Body.String() != replay.Body.String() {
		t.Fatal("replay did not return the original receipt")
	}
	var receipt posSale
	if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	actual, err := time.Parse(time.RFC3339Nano, receipt.Recorded)
	if err != nil {
		t.Fatal(err)
	}
	now = actual
	live := read(issue(`{"entity_id":1,"outlet_id":2}`))
	if live.SaleCount != 1 || live.TotalMinor != 25000 || len(live.Outlets) != 1 || live.Outlets[0].ID != 2 {
		t.Fatalf("replayed sale changed the local-month totals: %+v", live)
	}
}
