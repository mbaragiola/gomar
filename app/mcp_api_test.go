package main

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMCPMonthUsesBuenosAiresBoundary(t *testing.T) {
	utc := time.Date(2030, time.December, 1, 1, 30, 0, 0, time.UTC)
	from, to, err := mcpMonth(utc, "this_month")
	if err != nil || from.Format(time.RFC3339) != "2030-11-01T00:00:00-03:00" || to.Format(time.RFC3339) != "2030-12-01T00:00:00-03:00" {
		t.Fatalf("local reporting bounds: %s %s %v", from, to, err)
	}
}

func TestPostgresMCPReadsOnlyGrantedRecordedSales(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic scoped tool password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'test-operator',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO entities(id,name,cuit,active) VALUES(1,'Sale test','00000000001',TRUE),(2,'Foreign test','00000000002',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First test',TRUE),(2,1,'Ungrantable test',TRUE),(3,2,'Outside test',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
		INSERT INTO outlet_grants VALUES(1,1);
		INSERT INTO pos_sales(request_id,outlet_id,actor_id,total_minor,recorded_at) VALUES
		 ('50000000-0000-4000-8000-000000000001',1,1,101,'2030-11-01T03:00:00Z'),
		 ('50000000-0000-4000-8000-000000000002',1,1,299,'2030-11-01T03:00:00.123456789Z'),
		 ('50000000-0000-4000-8000-000000000003',1,1,700,'2030-11-01T02:59:59.999999999Z'),
		 ('50000000-0000-4000-8000-000000000004',1,1,800,'2030-12-01T03:00:00.001Z'),
		 ('50000000-0000-4000-8000-000000000005',2,1,900,'2030-11-15T12:00:00Z'),
		 ('50000000-0000-4000-8000-000000000006',3,1,950,'2030-11-15T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, time.November, 15, 13, 0, 0, 0, time.UTC)
	a := &app{db: db, host: "example.com", enableMCP: true, enablePOS: true, mcpClock: func() time.Time { return now }}
	mux := a.routes()
	cookies := postgresTestCookies(t, mux, "test-operator", password)
	invoke := func(method, path, body, token string, session bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if session {
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: got %d, want %d; cache %q", method, path, w.Code, want, w.Header().Get("Cache-Control"))
		}
		return w
	}
	const tokenPath = "/api/v1/auth/mcp-token"
	const me = "/api/v1/mcp/me"
	const sales = "/api/v1/mcp/entities/1/sales-summary?period=this_month"
	invoke("GET", me, "", "", true, 401) // A browser session is not an MCP credential.
	invoke("POST", tokenPath, `{"entity_id":1,"outlet_id":2}`, "", true, 404)
	issue := func(body string) (string, int64) {
		t.Helper()
		var c struct {
			Token string
			ID    int64
		}
		if err := json.Unmarshal(invoke("POST", tokenPath, body, "", true, 201).Body.Bytes(), &c); err != nil || len(c.Token) != 43 || c.ID <= 0 {
			t.Fatalf("issued credential format: %v", err)
		}
		return c.Token, c.ID
	}
	token, id := issue(`{"entity_id":1,"outlet_id":1}`)
	invoke("GET", me, "", token, false, 200)
	invoke("GET", "/api/v1/mcp/entities", "", token, false, 200)
	invoke("GET", "/api/v1/mcp/entities/1/outlets", "", token, false, 200)
	invoke("GET", "/api/v1/mcp/entities/2/outlets", "", token, false, 404)
	invoke("GET", sales+"&outlet_id=2", "", token, false, 404)
	invoke("GET", sales+"&period=previous_month", "", token, false, 422)
	var summary mcpSales
	if err := json.Unmarshal(invoke("GET", sales, "", token, false, 200).Body.Bytes(), &summary); err != nil || summary.Currency != "ARS" || summary.SaleCount != 2 || summary.TotalMinor != 400 || len(summary.Outlets) != 1 || summary.Outlets[0].ID != 1 {
		t.Fatalf("incorrect scoped summary %+v: %v", summary, err)
	}
	invoke("DELETE", tokenPath+"/"+strconv.FormatInt(id, 10), "", "", true, 204)
	invoke("GET", me, "", token, false, 401)
	token, _ = issue(`{"entity_id":1}`)
	now = now.Add(mcpTokenLifetime)
	invoke("GET", me, "", token, false, 401)
}
