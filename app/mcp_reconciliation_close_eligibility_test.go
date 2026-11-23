package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReconciliationCloseEligibilityChecksStateBeforeAggregate(t *testing.T) {
	// A nil transaction makes a mistaken aggregate lookup fail this test.
	for _, state := range []string{"draft", "closed", "deleted"} {
		if _, err := reconciliationClosureEligibility(nil, reconciliationReadRun{State: state}); err != reconciliationNotInReview {
			t.Fatalf("state %s: %v", state, err)
		}
	}
}

func TestPostgresMCPReconciliationCloseEligibility(t *testing.T) {
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
	const pass = "synthetic-recon-close-eligibility"
	exec("INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'reviewer',$1,TRUE,FALSE,FALSE)", encodePassword(pass))
	exec(`
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Other','00000000002',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Centro',TRUE),(2,1,'Norte',TRUE),(3,2,'Other Centro',TRUE),(4,1,'Dormant',FALSE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,4);
 INSERT INTO reconciliation_read_runs(id,entity_id,state,revision,rules_version,match_reference,created_at,confirmed_count)
 SELECT id,CASE WHEN id=8 THEN 2 ELSE 1 END,
 CASE id WHEN 2 THEN 'draft' WHEN 6 THEN 'closed' WHEN 9 THEN 'deleted' ELSE 'review' END,
 3,CASE WHEN id=10 THEN 2 ELSE 1 END,FALSE,'2026-11-01T00:00:00Z',CASE WHEN id=11 THEN 1 ELSE 0 END
 FROM generate_series(1,11) id;
 INSERT INTO reconciliation_read_outlets SELECT id,CASE WHEN id=8 THEN 3 ELSE 1 END FROM generate_series(1,11) id;
 INSERT INTO reconciliation_read_outlets VALUES(5,2),(7,4);
 INSERT INTO reconciliation_detail_events(run_id,ordinal,payload,actor_id,operation_key)
 SELECT id,1,'{"action":"prepare","private":"private-event"}',1,'prepared' FROM generate_series(1,11) id WHERE id<>3;
 INSERT INTO reconciliation_read_sources(id,run_id,side,label,mapping,formats,row_count)
 VALUES(1,1,'payments','private-source','{}','{}',1),(4,4,'payments','private-source','{}','{}',1);
 INSERT INTO reconciliation_detail_rows(run_id,id,source_id,outlet_id,record_number,payload)
 VALUES(1,1,1,1,1,'{"id":1,"outlet_id":1,"record_number":1,"amount":"secret-amount"}'),
 (4,1,4,1,1,'{"id":1,"outlet_id":1,"record_number":1}');
 INSERT INTO reconciliation_detail_groups(run_id,ordinal,payload) VALUES(1,1,'{}');
 INSERT INTO reconciliation_detail_members VALUES(1,1,1,1);
 `)
	mux := (&app{db: db, host: "example.com", enableMCP: true}).routes()
	cookies := postgresTestCookies(t, mux, "reviewer", pass)
	ask := func(handler http.Handler, method, path, body, bearer string, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
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
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return strings.TrimSpace(w.Body.String())
	}
	issue := func(scope string) string {
		var token struct{ Token string }
		out := ask(mux, "POST", "/api/v1/auth/mcp-token", scope, "", 201)
		if err := json.Unmarshal([]byte(out), &token); err != nil || token.Token == "" {
			t.Fatal("could not issue local MCP credential")
		}
		return token.Token
	}
	key := issue(`{"entity_id":1,"outlet_id":1}`)
	broad := issue(`{"entity_id":1}`)
	path := func(entity, outlet, id int) string {
		return fmt.Sprintf("/api/v1/mcp/entities/%d/outlets/%d/reconciliation-runs/%d/close-eligibility", entity, outlet, id)
	}
	for _, tc := range []struct {
		run     int
		blocker string
		native  int
	}{
		{1, "", 0}, {2, "not_in_review", 409}, {3, "unsupported_review_data", 503},
		{4, "unsupported_review_data", 503}, {6, "not_in_review", 409},
		{10, "unsupported_review_data", 503}, {11, "unsupported_review_data", 503},
	} {
		want := `{"eligible":true}`
		if tc.blocker != "" {
			want = fmt.Sprintf(`{"eligible":false,"blocker":%q}`, tc.blocker)
		}
		if tc.blocker != "not_in_review" {
			want = strings.TrimSuffix(want, "}") + fmt.Sprintf(`,"handoff_url":"/#conciliacion=1:1:%d"}`, tc.run)
		}
		if got := ask(mux, "GET", path(1, 1, tc.run), "", key, 200); got != want {
			t.Fatalf("run %d got %s want %s", tc.run, got, want)
		}
		if tc.native != 0 {
			ask(mux, "POST", fmt.Sprintf("/api/v1/entities/1/reconciliations/%d/close", tc.run), `{"expected_revision":0,"operation_key":"synthetic-no-write"}`, "", tc.native)
		}
	}
	missing := ask(mux, "GET", path(1, 1, 999), "", key, 404)
	for _, run := range []int{0, 5, 7, 8, 9} {
		if got := ask(mux, "GET", path(1, 1, run), "", key, 404); got != missing {
			t.Fatal("scope disclosed existence")
		}
	}
	if got := ask(mux, "GET", path(1, 1, 5), "", broad, 200); got != `{"eligible":true}` {
		t.Fatal(got)
	}
	ask(mux, "GET", path(1, 1, 7), "", broad, 404)
	ask(mux, "GET", path(1, 2, 1), "", key, 404)
	ask(mux, "GET", path(2, 1, 1), "", key, 404)
	ask(mux, "GET", path(1, 1, 1)+"?extra=true", "", key, 422)
	ask(mux, "GET", path(1, 1, 1), "", "", 401)
	off := (&app{db: db, host: "example.com"}).routes()
	ask(off, "GET", path(1, 1, 1), "", key, 404)
	exec("UPDATE memberships SET role='accounting' WHERE id=1")
	if ask(mux, "GET", path(1, 1, 1), "", key, 403) != ask(mux, "GET", path(1, 1, 999), "", key, 403) {
		t.Fatal("role checked after run lookup")
	}
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	ask(mux, "GET", path(1, 1, 1), "", key, 200)
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=2")
	ask(mux, "GET", path(1, 1, 5), "", broad, 404)
	browser := func(h http.Handler, path, bearer string, signedIn bool, want int) string {
		t.Helper()
		req := httptest.NewRequest("GET", "http://example.com"+path, nil).WithContext(ctx)
		if signedIn {
			for _, cookie := range cookies {
				req.AddCookie(cookie)
			}
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("unexpected native handoff status %d, want %d", rec.Code, want)
		}
		return strings.TrimSpace(rec.Body.String())
	}
	nativePath := func(entity, outlet, run string) string {
		return "/api/v1/entities/" + entity + "/outlets/" + outlet + "/reconciliations/" + run + "/close-handoff"
	}
	native := nativePath("1", "1", "1")
	browser(mux, native, key, false, 401)
	browser(mux, native, "", false, 401)
	if got := browser(mux, native, "", true, 200); got != `{"run_id":1,"state":"review","eligible":true}` {
		t.Fatal("unexpected native projection", got)
	}
	if got := browser(mux, nativePath("1", "1", "3"), "", true, 200); got != `{"run_id":3,"state":"review","eligible":false,"blocker":"unsupported_review_data"}` {
		t.Fatal("unexpected native blocker", got)
	}
	absent := browser(mux, nativePath("1", "1", "999"), "", true, 404)
	for _, denied := range []string{
		nativePath("1", "1", "2"), nativePath("1", "1", "5"), nativePath("1", "1", "6"),
		nativePath("1", "1", "7"), nativePath("1", "1", "9"), nativePath("2", "3", "8"),
		nativePath("1", "2", "1"), nativePath("1", "1", "01"), nativePath("1", "1", "%31"), native + "?query=1",
	} {
		if browser(mux, denied, "", true, 404) != absent {
			t.Fatal("unexpected disclosure for", denied)
		}
	}
	browser(mux, native, key, true, 404)
	browser(off, native, "", true, 404)
	exec("UPDATE memberships SET role='accounting' WHERE id=1")
	if browser(mux, native, "", true, 404) != absent {
		t.Fatal("role denial disclosed run")
	}
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1")
	if browser(mux, native, "", true, 404) != absent {
		t.Fatal("grant revocation disclosed run")
	}
	exec("INSERT INTO outlet_grants VALUES(1,1)")
	var revisions, events, decisions, closed int
	err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM reconciliation_read_runs WHERE revision<>3),
 (SELECT count(*) FROM reconciliation_detail_events),(SELECT count(*) FROM reconciliation_review_decisions),
 (SELECT count(*) FROM reconciliation_read_runs WHERE state='closed')`).Scan(&revisions, &events, &decisions, &closed)
	if err != nil || revisions != 0 || events != 10 || decisions != 0 || closed != 1 {
		t.Fatalf("read modified the reconciliation: %d/%d/%d/%d: %v", revisions, events, decisions, closed, err)
	}
}
