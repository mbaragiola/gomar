package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestPostgresTenantSalesExport(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	seed := func(stmt string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture tenant','00000000001',TRUE),(2,'Other tenant','00000000002',TRUE);
	INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Export branch',TRUE),(2,1,'Ungrant branch',TRUE),(3,2,'Other branch',TRUE);
	INSERT INTO users(id,username,password,active,staff,superuser)
	VALUES(1,'fixture-accounting','!synthetic',TRUE,FALSE,FALSE),(2,'another-tenant','!synthetic',TRUE,FALSE,FALSE),
 (3,'same-branch','!synthetic',TRUE,FALSE,FALSE),(4,'read-only','!synthetic',TRUE,FALSE,FALSE);
	INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES (1,1,1,'accounting',TRUE),(2,2,2,'administration',TRUE),(3,3,1,'accounting',TRUE),(4,4,1,'read_only',TRUE);
	INSERT INTO outlet_grants VALUES(1,1),(2,3),(3,1),(4,1);
	INSERT INTO pos_sales(id,request_id,outlet_id,actor_id,total_minor,recorded_at)
	VALUES (11,'11000000-0000-4000-8000-000000000011',1,1,5,'2026-11-20T02:59:59.999Z'),
	       (12,'11000000-0000-4000-8000-000000000012',1,1,12345,'2026-11-20T03:00:00Z'),
	       (13,'11000000-0000-4000-8000-000000000013',2,1,987654,'2026-11-20T03:00:00Z'),
	       (14,'11000000-0000-4000-8000-000000000014',3,2,987654,'2026-11-20T03:00:00Z'),
	       (15,'11000000-0000-4000-8000-000000000015',1,1,5,'2026-11-21T03:00:00Z');`)
	a := &app{db: db, host: "example.com", enableMCP: true}
	router := a.routes()
	owner := nativeDocumentSession(t, a, router, 1)
	other := nativeDocumentSession(t, a, router, 2)
	request := func(cookies []*http.Cookie) *http.Request {
		r := httptest.NewRequest("POST", "http://example.com/api/v1/fixture-export", nil).WithContext(ctx)
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		return r
	}
	scope := tenantSalesExportScope{entity: 1, outlet: 1, requestID: "12000000-0000-4000-8000-000000000001"}
	const day = "2026-11-20"
	actor := user{id: 1}
	check := func(got, want error) {
		t.Helper()
		if !errors.Is(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	_, err := a.prepareTenantSalesExport(request(other), user{id: 2}, scope, day, day)
	check(err, missingEntity)
	_, err = a.prepareTenantSalesExport(request(other), actor, scope, day, day)
	check(err, unauthenticated)
	otherBranch := scope
	otherBranch.outlet = 2
	_, err = a.prepareTenantSalesExport(request(owner), actor, otherBranch, day, day)
	check(err, missingOutlet)
	_, err = a.prepareTenantSalesExport(request(owner), actor, scope, "2026-10-01", day)
	check(err, badInput)

	// A failed audit insert must leave neither a receipt nor returned bytes.
	seed(`CREATE FUNCTION fixture_reject_export() RETURNS trigger LANGUAGE plpgsql AS $test$
	BEGIN RAISE EXCEPTION 'fixture interrupted'; END; $test$;
	CREATE TRIGGER fixture_reject_export AFTER INSERT ON tenant_sales_exports FOR EACH ROW EXECUTE FUNCTION fixture_reject_export();`)
	result, err := a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	if err == nil || len(result.CSV) != 0 {
		t.Fatal("failed receipt returned an artifact")
	}
	seed("DROP TRIGGER fixture_reject_export ON tenant_sales_exports")
	result, err = a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, nil)
	expected := []byte("registrado_utc,importe_centavos_ars\n2026-11-20T03:00:00Z,12345\n")
	digest := sha256.Sum256(expected)
	if !bytes.Equal(result.CSV, expected) || result.Digest != hex.EncodeToString(digest[:]) || result.Acknowledged {
		t.Fatalf("wrong artifact or premature acknowledgment: %q %+v", result.CSV, result)
	}
	seed(`INSERT INTO pos_sales(id,request_id,outlet_id,actor_id,total_minor,recorded_at)
	VALUES(16,'11000000-0000-4000-8000-000000000016',1,1,2222,'2026-11-20T04:00:00Z')`)
	replay, err := a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, nil)
	if !bytes.Equal(replay.CSV, result.CSV) || replay.Digest != result.Digest || replay.Acknowledged {
		t.Fatal("late sales changed the prepared artifact")
	}
	_, err = a.prepareTenantSalesExport(request(owner), actor, scope, day, "2026-11-21")
	check(err, tenantSalesExportConflict)
	check(a.acknowledgeTenantSalesExport(request(owner), actor, scope, strings.Repeat("0", 64)), tenantSalesExportConflict)
	check(a.acknowledgeTenantSalesExport(request(other), user{id: 2}, scope, result.Digest), missingEntity)
	otherScope := scope
	otherScope.entity, otherScope.outlet = 2, 3
	check(a.acknowledgeTenantSalesExport(request(other), user{id: 2}, otherScope, result.Digest), missingTenantSalesExport)
	seed("UPDATE memberships SET role='read_only' WHERE id=1")
	check(a.acknowledgeTenantSalesExport(request(owner), actor, scope, result.Digest), forbiddenRole)
	seed("UPDATE memberships SET role='accounting' WHERE id=1; DELETE FROM outlet_grants WHERE membership_id=1")
	_, err = a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, missingOutlet)
	seed("INSERT INTO outlet_grants VALUES(1,1)")
	check(a.acknowledgeTenantSalesExport(request(owner), actor, scope, result.Digest), nil)
	check(a.acknowledgeTenantSalesExport(request(owner), actor, scope, result.Digest), nil)
	replay, err = a.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, nil)
	if !replay.Acknowledged || !bytes.Equal(replay.CSV, result.CSV) {
		t.Fatal("explicit acknowledgment did not bind to original digest")
	}
	hosted := &app{db: db, host: "example.com", hostedDiscoveryRead: &hostedDiscoveryReadFixture{}}
	_, err = hosted.prepareTenantSalesExport(request(owner), actor, scope, day, day)
	check(err, missingOutlet)
	withoutCSRF := request(owner)
	withoutCSRF.Header.Del("X-CSRFToken")
	check(a.acknowledgeTenantSalesExport(withoutCSRF, actor, scope, result.Digest), badCSRF)
	var receipts, acknowledgments int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM tenant_sales_exports),
		(SELECT COUNT(*) FROM tenant_sales_export_acknowledgments)`).Scan(&receipts, &acknowledgments); err != nil ||
		receipts != 1 || acknowledgments != 1 {
		t.Fatalf("wrong audit counts: %d %d %v", receipts, acknowledgments, err)
	}
	testTenantSalesExportDelivery(t, a, router, owner, other, seed)

}

func testTenantSalesExportDelivery(t *testing.T, a *app, router http.Handler, owner, other []*http.Cookie, seed func(string)) {
	t.Helper()
	sameBranch := nativeDocumentSession(t, a, router, 3)
	readOnly := nativeDocumentSession(t, a, router, 4)
	ask := func(handler http.Handler, method, path, body, bearer string, cookies []*http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if method == http.MethodPost {
			if strings.HasPrefix(body, "{") {
				r.Header.Set("Content-Type", "application/json")
				for _, cookie := range cookies {
					if cookie.Name == "csrftoken" {
						r.Header.Set("X-CSRFToken", cookie.Value)
					}
				}
			} else {
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	readField := func(page, name string) string {
		t.Helper()
		match := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(page)
		if match == nil {
			t.Fatalf("missing form field %s", name)
		}
		return match[1]
	}
	token := func(cookies []*http.Cookie, inputs string) string {
		t.Helper()
		w := ask(router, http.MethodPost, "/api/v1/auth/mcp-token", inputs, "", cookies, 201)
		var response struct{ Token string }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Token == "" {
			t.Fatal("fixture read token missing")
		}
		return response.Token
	}
	const native = "/exportaciones/ventas/1/1"
	const mcpPath = "/api/v1/mcp/entities/1/outlets/1/sales-export-handoff"
	pinned := token(owner, `{"entity_id":1,"outlet_id":1}`)
	wide := token(owner, `{"entity_id":1}`)
	limited := token(readOnly, `{"entity_id":1,"outlet_id":1}`)
	if w := ask(router, http.MethodGet, mcpPath, "", pinned, nil, 200); strings.TrimSpace(w.Body.String()) != `{"handoff_url":"`+native+`"}` {
		t.Fatalf("handoff returned more than the page path: %s", w.Body)
	}
	for _, denial := range []struct {
		path, bearer string
		code         int
	}{
		{mcpPath, "", 401},
		{mcpPath, limited, 403},
		{mcpPath, wide, 404},
		{"/api/v1/mcp/entities/1/outlets/2/sales-export-handoff", pinned, 404},
		{"/api/v1/mcp/entities/2/outlets/3/sales-export-handoff", pinned, 404},
	} {
		w := ask(router, http.MethodGet, denial.path, "", denial.bearer, nil, denial.code)
		if strings.Contains(w.Body.String(), "handoff_url") || strings.Contains(w.Body.String(), "importe_centavos") {
			t.Fatal("denied MCP handoff disclosed protected data")
		}
	}
	hosted := (&app{db: a.db, host: a.host, enableMCP: true, hostedDiscoveryRead: &hostedDiscoveryReadFixture{}}).routes()
	ask(hosted, http.MethodGet, mcpPath, "", pinned, nil, 404)
	ask(hosted, http.MethodGet, native, "", "", owner, 404)
	ask(router, http.MethodGet, native, "", pinned, nil, 401)
	ask(router, http.MethodGet, native, "", pinned, owner, 422)
	ask(router, http.MethodGet, native, "", "", readOnly, 403)
	ask(router, http.MethodGet, native, "", "", other, 404)
	ask(router, http.MethodGet, "/exportaciones/ventas/1/2", "", "", owner, 404)
	page := ask(router, http.MethodGet, native, "", "", owner, 200)
	fields := url.Values{
		"csrfmiddlewaretoken": {readField(page.Body.String(), "csrfmiddlewaretoken")},
		"request_id":          {readField(page.Body.String(), "request_id")},
		"from":                {"2026-11-20"},
		"through":             {"2026-11-20"},
		"action":              {"prepare"},
	}
	prepared := ask(router, http.MethodPost, native, fields.Encode(), "", owner, 200)
	if !strings.Contains(prepared.Body.String(), "Descargar CSV") || strings.Contains(prepared.Body.String(), "registrado_utc") {
		t.Fatal("preparation did not lead to a private download")
	}
	fields.Set("action", "download")
	first := ask(router, http.MethodPost, native, fields.Encode(), "", owner, 200)
	if first.Header().Get("Cache-Control") != "private, no-store" || first.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
		first.Header().Get("Content-Disposition") != `attachment; filename="ventas.csv"` ||
		first.Body.String() != "registrado_utc,importe_centavos_ars\n2026-11-20T03:00:00Z,12345\n2026-11-20T04:00:00Z,2222\n" {
		t.Fatal("download returned the wrong private attachment")
	}
	retry := ask(router, http.MethodPost, native, fields.Encode(), "", owner, 200)
	if !bytes.Equal(first.Body.Bytes(), retry.Body.Bytes()) {
		t.Fatal("retry changed the bytes")
	}
	digest := sha256.Sum256(first.Body.Bytes())
	if got := readField(prepared.Body.String(), "digest"); got != hex.EncodeToString(digest[:]) {
		t.Fatal("confirmation did not refer to the downloaded bytes")
	}
	fields.Set("action", "acknowledge")
	fields.Set("digest", hex.EncodeToString(digest[:]))
	ask(router, http.MethodPost, native, fields.Encode(), "", owner, 422)
	fields.Set("received", "yes")
	ask(router, http.MethodPost, native, fields.Encode(), "", sameBranch, 403)
	samePage := ask(router, http.MethodGet, native, "", "", sameBranch, 200)
	fields.Set("csrfmiddlewaretoken", readField(samePage.Body.String(), "csrfmiddlewaretoken"))
	ask(router, http.MethodPost, native, fields.Encode(), "", sameBranch, 404)
	fields.Set("csrfmiddlewaretoken", readField(page.Body.String(), "csrfmiddlewaretoken"))
	seed("UPDATE memberships SET role='read_only' WHERE id=1")
	ask(router, http.MethodGet, mcpPath, "", pinned, nil, 403)
	ask(router, http.MethodPost, native, fields.Encode(), "", owner, 403)
	seed("UPDATE memberships SET role='accounting' WHERE id=1")
	for range 2 {
		done := ask(router, http.MethodPost, native, fields.Encode(), "", owner, 200)
		if !strings.Contains(done.Body.String(), "Confirmación de recepción registrada.") {
			t.Fatal("explicit acknowledgment missing")
		}
	}
}
