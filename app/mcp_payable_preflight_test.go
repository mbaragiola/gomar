package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPayableDocumentEligibilityOrder(t *testing.T) {
	for _, tc := range []struct {
		posted       bool
		status, kind string
		total        int64
		matches      bool
		want         string
		code         int
	}{
		{true, "draft", "credit_note", 0, false, "already_posted", 409},
		{false, "draft", "credit_note", 0, false, "not_reviewed", 422},
		{false, "reviewed", "credit_note", 0, false, "not_invoice", 422},
		{false, "reviewed", "invoice", 0, false, "nonpositive_total", 422},
		{false, "reviewed", "invoice", maxPayableMinor + 1, false, "total_out_of_range", 422},
		{false, "reviewed", "invoice", maxPayableMinor, false, "supplier_mismatch", 422},
		{false, "reviewed", "invoice", maxPayableMinor, true, "", 0},
	} {
		got, err := payableDocumentEligibility(tc.posted, tc.status, tc.kind, tc.total, tc.matches)
		if got != tc.want || tc.code == 0 && err != nil {
			t.Fatalf("got blocker %q error %v; want %q", got, err, tc.want)
		}
		if tc.code != 0 {
			if p, ok := err.(problem); !ok || p.status != tc.code {
				t.Fatalf("got error %v; want status %d", err, tc.code)
			}
		}
	}
}

func TestPostgresMCPPayablePreflightUsesNativeGateWithoutWrites(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	const pass = "synthetic-payable-preflight"
	exec("INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'accountant',$1,TRUE,FALSE,FALSE)", encodePassword(pass))
	exec(`
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Other','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',TRUE),(2,1,'Norte',TRUE),(3,2,'Other Centro',TRUE),(4,1,'Dormant',FALSE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'accounting',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,4);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'private-vendor','00000000003',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,
 currency,total_minor,description,status,revision,created_at,updated_at)
 SELECT id,1,CASE id WHEN 8 THEN 2 WHEN 9 THEN 4 ELSE 1 END,1,
 CASE id WHEN 6 THEN '00000000099' ELSE '00000000003' END,
 CASE id WHEN 3 THEN 'credit_note' ELSE 'invoice' END,'A',1,id,'2026-11-01','ARS',
 CASE id WHEN 4 THEN 0 WHEN 5 THEN 100000000000000 ELSE 10000 END,'private-document',
 CASE id WHEN 2 THEN 'draft' ELSE 'reviewed' END,1,
 '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z' FROM generate_series(1,10) id;
 UPDATE documents SET deleted_at='2026-11-02T00:00:00Z' WHERE id=10;
 INSERT INTO ap_payables(entity_id,outlet_id,supplier_id,source_document_id,currency,original_minor,balance_minor,
 supplier_name_snapshot,supplier_cuit_snapshot,document_reference_snapshot,issue_date_snapshot,posted_by_id,posted_at)
 VALUES(1,1,1,7,'ARS',10000,10000,'private-vendor','00000000003','private-document','2026-11-01',1,'2026-11-01T00:00:00Z');
 `)
	mux := (&app{db: db, host: "example.com", enableMCP: true}).routes()
	cookies := postgresTestCookies(t, mux, "accountant", pass)
	ask := func(handler http.Handler, method, path, body, token string, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
			for _, cookie := range cookies {
				req.AddCookie(cookie)
				if cookie.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", cookie.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d: %s", method, path, w.Code, want, w.Body.String())
		}
		return strings.TrimSpace(w.Body.String())
	}
	wire := ask(mux, "POST", "/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, "", 201)
	var credentials struct{ Token string }
	if err := json.Unmarshal([]byte(wire), &credentials); err != nil || credentials.Token == "" {
		t.Fatal("synthetic token missing")
	}
	url := func(entity, outlet, id int) string {
		return fmt.Sprintf("/api/v1/mcp/entities/%d/outlets/%d/documents/%d/payable-preflight", entity, outlet, id)
	}
	for _, tc := range []struct {
		id      int
		blocker string
	}{
		{1, ""}, {2, "not_reviewed"}, {3, "not_invoice"}, {4, "nonpositive_total"},
		{5, "total_out_of_range"}, {6, "supplier_mismatch"}, {7, "already_posted"},
	} {
		want := `{"eligible":true,"handoff_url":"/#alta-cuenta=1:1:1"}`
		if tc.blocker != "" {
			want = fmt.Sprintf(`{"eligible":false,"blocker":%q}`, tc.blocker)
		}
		if got := ask(mux, "GET", url(1, 1, tc.id), "", credentials.Token, 200); got != want {
			t.Fatalf("unexpected response: got %s want %s", got, want)
		}
		if tc.blocker != "" {
			nativePath := fmt.Sprintf("/api/v1/entities/1/documents/%d/payable", tc.id)
			status := 422
			if tc.id == 7 {
				status = 409
			}
			ask(mux, "POST", nativePath, `{"motivo":"QA synthetic reason"}`, "", status)
		}
	}
	missing := ask(mux, "GET", url(1, 1, 999), "", credentials.Token, 404)
	for _, id := range []int{8, 9, 10, 0} {
		if got := ask(mux, "GET", url(1, 1, id), "", credentials.Token, 404); got != missing {
			t.Fatal("existence disclosure")
		}
	}
	ask(mux, "GET", url(1, 2, 8), "", credentials.Token, 404)
	ask(mux, "GET", url(2, 1, 1), "", credentials.Token, 404)
	ask(mux, "GET", url(1, 1, 1)+"?extra=1", "", credentials.Token, 422)
	ask(mux, "GET", url(1, 1, 1), "", "", 401)
	var broad struct{ Token string }
	wire = ask(mux, "POST", "/api/v1/auth/mcp-token", `{"entity_id":1}`, "", 201)
	if err := json.Unmarshal([]byte(wire), &broad); err != nil || broad.Token == "" {
		t.Fatal("synthetic broad token missing")
	}
	if got := ask(mux, "GET", url(1, 1, 1), "", broad.Token, 200); got != `{"eligible":true}` {
		t.Fatalf("wide key unexpectedly received a handoff: %s", got)
	}

	landingURL := func(entity, outlet, id int) string {
		return fmt.Sprintf("/api/v1/entities/%d/outlets/%d/documents/%d/payable-eligibility-handoff", entity, outlet, id)
	}
	landing := func(handler http.Handler, url, bearer string, signedIn bool, want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", "http://example.com"+url, nil).WithContext(ctx)
		if signedIn {
			for _, cookie := range cookies {
				r.AddCookie(cookie)
			}
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s = %d %s (wanted %d private no-store)", url, w.Code, w.Body.String(), want)
		}
		if signedIn && w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("expected no-referrer")
		}
		return strings.TrimSpace(w.Body.String())
	}
	nativeURL := landingURL(1, 1, 1)
	if got := landing(mux, nativeURL, "", true, 200); got != `{"document_id":1,"printed_reference":{"document_type":"invoice","letter":"A","point_of_sale":1,"number":1}}` {
		t.Fatalf("unexpected native handoff projection: %s", got)
	}
	unavailable := landing(mux, landingURL(1, 1, 999), "", true, 404)
	for _, q := range []string{nativeURL + "?", nativeURL + "?extra=1", landingURL(1, 2, 1), landingURL(2, 1, 1),
		landingURL(1, 4, 9), strings.Replace(nativeURL, "/documents/1/", "/documents/01/", 1),
		landingURL(1, 1, 2), landingURL(1, 1, 3), landingURL(1, 1, 4), landingURL(1, 1, 5), landingURL(1, 1, 6), landingURL(1, 1, 7)} {
		if got := landing(mux, q, "", true, 404); got != unavailable {
			t.Fatalf("ineligible/stale target disclosed a distinction: %s", got)
		}
	}
	landing(mux, nativeURL, "", false, 401)
	landing(mux, nativeURL, credentials.Token, false, 401)
	landing(mux, nativeURL, credentials.Token, true, 404)
	exec("UPDATE documents SET outlet_id=2 WHERE id=1")
	if got := landing(mux, nativeURL, "", true, 404); got != unavailable {
		t.Fatalf("stale moved target response: %s", got)
	}
	exec("UPDATE documents SET outlet_id=1,point_of_sale=0 WHERE id=1")
	if got := ask(mux, "GET", url(1, 1, 1), "", credentials.Token, 200); got != `{"eligible":true}` {
		t.Fatalf("unsupported reference produced a handoff or changed old eligibility: %s", got)
	}
	landing(mux, nativeURL, "", true, 404)
	exec("UPDATE documents SET point_of_sale=1 WHERE id=1")
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1")
	landing(mux, nativeURL, "", true, 404)
	exec("INSERT INTO outlet_grants VALUES (1,1)")
	disabled := (&app{db: db, host: "example.com"}).routes()
	ask(disabled, "GET", url(1, 1, 1), "", credentials.Token, 404)
	landing(disabled, nativeURL, "", true, 404)
	for _, role := range []string{"read_only", "purchasing"} {
		exec("UPDATE memberships SET role=$1 WHERE id=1", role)
		landing(mux, nativeURL, "", true, 404)
		if got := ask(mux, "GET", url(1, 1, 1), "", credentials.Token, 403); got != ask(mux, "GET", url(1, 1, 999), "", credentials.Token, 403) {
			t.Fatal("role denied after lookup")
		}
	}
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	ask(mux, "GET", url(1, 1, 1), "", credentials.Token, 200)
	exec("UPDATE outlets SET active=FALSE WHERE id=1")
	ask(mux, "GET", url(1, 1, 1), "", credentials.Token, 404)
	var payables, movements, events, changed int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM ap_payables),(SELECT count(*) FROM ap_ledger),(SELECT count(*) FROM ap_financial_events),(SELECT count(*) FROM documents WHERE revision<>1)`).Scan(&payables, &movements, &events, &changed); err != nil || payables != 1 || movements != 0 || events != 0 || changed != 0 {
		t.Fatalf("read changed payable state: %d/%d/%d/%d: %v", payables, movements, events, changed, err)
	}
}
