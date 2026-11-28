package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPostgresAdminManagementAndSuppliers(t *testing.T) {
	ctx, db := testPostgresDB(t)
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regnamespace('gomar') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("requires a fresh QA database; already migrated=%t: %v", exists, err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	password := "synthetic-admin-password"
	hash := encodePassword(password)
	for _, seed := range []struct {
		q    string
		args []any
	}{
		{"INSERT INTO users(id,username,password,active,staff,superuser) VALUES (101,'admin-fixture',$1,TRUE,TRUE,TRUE),(102,'clerk-fixture',$1,TRUE,FALSE,FALSE),(103,'tenant-owner',$1,TRUE,FALSE,FALSE),(104,'new-tenant-owner',$1,TRUE,FALSE,FALSE)", []any{hash}},
		{"INSERT INTO entities(id,name,cuit,active) VALUES (101,'Tenant One','00000000000',TRUE),(102,'Tenant Two','00000000019',TRUE),(104,'Initial tenant','00000000055',TRUE)", nil},
		{"INSERT INTO outlets(id,entity_id,name,active) VALUES (101,101,'Center',TRUE),(102,102,'Foreign',TRUE),(103,101,'Private branch',TRUE)", nil},
		{"INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES (101,101,101,'administration',TRUE),(103,103,101,'administration',TRUE),(104,104,104,'administration',TRUE)", nil},
		{"INSERT INTO outlet_grants(membership_id,outlet_id) VALUES (101,101),(103,101)", nil},
	} {
		if _, err := db.ExecContext(ctx, seed.q, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{db: db, host: "example.com", enableMCP: true}
	handler := a.routes()
	cookies := map[string]*http.Cookie{}
	runAs := func(session []*http.Cookie, token, method, path, body, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		for _, cookie := range session {
			r.AddCookie(cookie)
			if cookie.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	run := func(method, path, body, contentType string) *httptest.ResponseRecorder {
		session := make([]*http.Cookie, 0, len(cookies))
		for _, cookie := range cookies {
			session = append(session, cookie)
		}
		return runAs(session, "", method, path, body, contentType)
	}
	check := func(w *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
		t.Helper()
		if w.Code != status {
			t.Fatalf("response status=%d want=%d body=%s", w.Code, status, w.Body)
		}
		return w
	}
	for _, cookie := range check(run("GET", "/api/v1/auth/csrf", "", ""), 200).Result().Cookies() {
		cookies[cookie.Name] = cookie
	}
	for _, cookie := range check(run("POST", "/api/v1/auth/login", `{"usuario":"admin-fixture","clave":"`+password+`"}`, "application/json"), 200).Result().Cookies() {
		cookies[cookie.Name] = cookie
	}
	api := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		return check(run(method, "/api/v1/entities/101"+path, body, "application/json"), status)
	}
	api("GET", "/management", "", 200)
	api("POST", "/memberships", `{"usuario":"clerk-fixture","rol":"purchasing","activa":true,"outlet_ids":[102]}`, 403)
	const crew = `{"usuario":"clerk-fixture","rol":"purchasing","activa":true,"outlet_ids":[101]}`
	ch := make(chan int, 2)
	for range 2 {
		go func() { ch <- run("POST", "/api/v1/entities/101/memberships", crew, "application/json").Code }()
	}
	x, y := <-ch, <-ch
	if !(x == 201 && y == 409 || x == 409 && y == 201) {
		t.Fatalf("concurrent create = %d, %d; want one commit and one conflict", x, y)
	}
	var memberID int64
	if err := db.QueryRowContext(ctx, "SELECT id FROM memberships WHERE user_id=102 AND entity_id=101").Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	api("PUT", fmt.Sprintf("/memberships/%d", memberID), `{"rol":"read_only","activa":true,"outlet_ids":[101]}`, 200)
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM access_events WHERE membership_id=$1", memberID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("unexpected membership audit count %d: %v", n, err)
	}
	api("PUT", "", `{"nombre":" Tenant renamed ","cuit":"00-00000003-5"}`, 200)
	api("PUT", "", `{"nombre":"Duplicate","cuit":"00000000019"}`, 409)
	api("PUT", "", `{"nombre":"Invalid","cuit":"00000000001"}`, 422)
	owner := postgresTestCookies(t, handler, "tenant-owner", password)
	initialOwner := postgresTestCookies(t, handler, "new-tenant-owner", password)
	const outletEndpoint = "/api/v1/entities/101/outlets"
	ownerCall := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		return check(runAs(owner, "", method, path, body, "application/json"), status)
	}
	api("POST", "/outlets", `{"nombre":"Platform self-grant attempt"}`, 403)
	ownerCall("POST", "/api/v1/entities/102/outlets", `{"nombre":"Foreign create"}`, 404)
	createdOutlet := ownerCall("POST", outletEndpoint, `{"nombre":" New outlet ","activa":true}`, 201)
	var added managedOutlet
	if err := json.Unmarshal(createdOutlet.Body.Bytes(), &added); err != nil || added.ID < 1 || !added.Authorized {
		t.Fatalf("missing created outlet ID: %v", err)
	}
	ownerCall("POST", outletEndpoint, `{"nombre":"New outlet","activa":true}`, 409)
	api("PUT", "/outlets/101", `{"nombre":" New outlet ","activa":true}`, 409)
	api("PUT", "/outlets/101", `{"nombre":" Center renamed ","activa":false}`, 200)
	api("PUT", "/outlets/102", `{"nombre":"Foreign edit","activa":false}`, 404)
	api("PUT", "/outlets/103", `{"nombre":"Out-of-scope edit","activa":false}`, 404)
	check(run("PUT", "/api/v1/entities/102", `{"nombre":"Foreign edit","cuit":"00000000019"}`, "application/json"), 404)
	check(run("GET", "/api/v1/entities/102/catalog-events", "", ""), 404)
	api("GET", "/catalog-events", "", 404)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tenant_catalog_events
		WHERE entity_id=101 AND occurred_at <= clock_timestamp() AND (
		 (action='update_entity' AND actor_id=101 AND outlet_id IS NULL AND before='{"name":"Tenant One","cuit":"00000000000"}'
		  AND after='{"name":"Tenant renamed","cuit":"00000000035"}')
		 OR (action='create_outlet' AND actor_id=103 AND outlet_id=$1 AND before='{}' AND after='{"name":"New outlet","active":true}')
		 OR (action='update_outlet' AND actor_id=101 AND outlet_id=101 AND before='{"name":"Center","active":true}'
		  AND after='{"name":"Center renamed","active":false}'))`, added.ID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("unexpected tenant catalog audit snapshots: %d, %v", n, err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM tenant_catalog_events"); err == nil {
		t.Fatal("tenant catalog events can be deleted")
	}
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION gomar.qa_reject_catalog_audit() RETURNS trigger
		LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic catalog audit failure'; END;$$;
		CREATE TRIGGER qa_reject_catalog_audit BEFORE INSERT ON tenant_catalog_events
		FOR EACH ROW EXECUTE FUNCTION gomar.qa_reject_catalog_audit()`); err != nil {
		t.Fatal(err)
	}
	api("PUT", "", `{"nombre":"Uncommitted entity","cuit":"00000000035"}`, 500)
	ownerCall("POST", outletEndpoint, `{"nombre":"Uncommitted outlet"}`, 500)
	api("PUT", "/outlets/101", `{"nombre":"Uncommitted rename","activa":true}`, 500)
	var rolledBack bool
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM tenant_catalog_events)=3 AND
		EXISTS(SELECT 1 FROM entities WHERE id=101 AND name='Tenant renamed' AND cuit='00000000035') AND
		EXISTS(SELECT 1 FROM outlets WHERE id=101 AND name='Center renamed' AND NOT active) AND
		NOT EXISTS(SELECT 1 FROM outlets WHERE entity_id=101 AND name='Uncommitted outlet')`).Scan(&rolledBack); err != nil || !rolledBack {
		t.Fatalf("catalog write or audit persisted on failure: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER qa_reject_catalog_audit ON tenant_catalog_events;
		DROP FUNCTION gomar.qa_reject_catalog_audit()`); err != nil {
		t.Fatal(err)
	}
	api("PUT", "/outlets/101", `{"nombre":"Center renamed","activa":true}`, 200)
	ownerCall("PUT", fmt.Sprintf("/api/v1/entities/101/memberships/%d", memberID),
		fmt.Sprintf(`{"rol":"read_only","activa":true,"outlet_ids":[101,%d]}`, added.ID), 200)
	ownerCall("PUT", fmt.Sprintf("/api/v1/entities/101/memberships/%d", memberID),
		`{"rol":"read_only","activa":true,"outlet_ids":[103]}`, 403)
	check(runAs(initialOwner, "", "GET", "/api/v1/entities/104/outlets", "", ""), 200)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM access_events WHERE entity_id=101
		AND membership_id=103 AND actor_id=103 AND action='update_membership'
		AND before::jsonb->'outlet_ids'='[101]' AND after::jsonb->'outlet_ids'=to_jsonb(ARRAY[$1::bigint,101])
		AND after::jsonb->>'role'='administration'`, added.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("missing native first grant audit: %d, %v", n, err)
	}
	const supplierPayload = `{"razon_social":"Test supplier","cuit":"00000000000"}`
	w := api("POST", "/suppliers", supplierPayload, 201)
	var item supplier
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil || item.ID < 1 {
		t.Fatalf("bad created supplier: %+v %v", item, err)
	}
	api("POST", "/suppliers", supplierPayload, 409)
	api("PUT", fmt.Sprintf("/suppliers/%d", item.ID), `{"razon_social":"Renamed","cuit":"00000000000"}`, 200)
	if w := api("GET", "/suppliers", "", 200); !strings.Contains(w.Body.String(), "Renamed") {
		t.Fatalf("supplier update missing from list: %s", w.Body)
	}
	check(run("GET", adminSupplierPath, "", ""), 200)
	check(run("GET", adminSupplierPath+fmt.Sprintf("%d/change/", item.ID), "", ""), 200)
	postForm := func(path string, fields url.Values) *httptest.ResponseRecorder {
		return run(http.MethodPost, path, fields.Encode(), "application/x-www-form-urlencoded")
	}
	check(postForm(adminSupplierAddPath, url.Values{
		"entity": {"101"}, "business_name": {"Catalog test supplier"}, "cuit": {"00000000019"}, "active": {"on"},
	}), 302)
	adminAdd := func(entity, membership, name string, active bool) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"entity": {entity}, "name": {name}}
		if membership != "" {
			form.Set("initial_membership", membership)
		}
		if active {
			form.Set("active", "on")
		}
		return postForm(adminOutletAddPath, form)
	}
	adminForm := check(run("GET", adminOutletAddPath, "", ""), 200).Body.String()
	if !strings.Contains(adminForm, "Initial tenant · new-tenant-owner") ||
		strings.Contains(adminForm, "Tenant renamed · admin-fixture") {
		t.Fatal("platform form offered an ineligible tenant recipient")
	}
	for _, bad := range []string{"", "103", "101"} {
		check(adminAdd("104", bad, "Initial branch", true), 200)
	}
	check(adminAdd("104", "104", "Initial branch", true), 302)
	check(adminAdd("104", "104", "Inactive branch", false), 302)
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION gomar.qa_reject_initial_outlet_grant() RETURNS trigger
		LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic grant audit failure'; END;$$;
		CREATE TRIGGER qa_reject_initial_outlet_grant BEFORE INSERT ON access_events
		FOR EACH ROW EXECUTE FUNCTION gomar.qa_reject_initial_outlet_grant()`); err != nil {
		t.Fatal(err)
	}
	check(adminAdd("104", "104", "Unaudited branch", true), 500)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outlets WHERE entity_id=104`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("failed grant audit left an outlet: %d, %v", n, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER qa_reject_initial_outlet_grant ON access_events;
		DROP FUNCTION gomar.qa_reject_initial_outlet_grant()`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Initial branch", "Inactive branch"} {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
			WHERE o.entity_id=104 AND o.name=$1 AND g.membership_id=104`, name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing platform first grant for %s: %d, %v", name, n, err)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM access_events
		WHERE entity_id=104 AND membership_id=104 AND actor_id=101 AND action='update_membership'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("missing platform grant audit: %d, %v", n, err)
	}
	nativeList := check(runAs(initialOwner, "", "GET", "/api/v1/entities/104/outlets", "", ""), 200).Body.String()
	tokenResponse := check(runAs(initialOwner, "", "POST", "/api/v1/auth/mcp-token", `{"entity_id":104}`, "application/json"), 201)
	var key struct{ Token string }
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &key); err != nil || key.Token == "" {
		t.Fatalf("unavailable synthetic MCP key: %v", err)
	}
	mcpList := check(runAs(nil, key.Token, "GET", "/api/v1/mcp/entities/104/outlets", "", ""), 200).Body.String()
	for _, list := range []string{nativeList, mcpList} {
		if !strings.Contains(list, "Initial branch") || strings.Contains(list, "Inactive branch") {
			t.Fatal("active/inactive grant visibility differs")
		}
	}
	deniedAdmin := check(runAs(initialOwner, "", "POST", adminOutletAddPath, "entity=104&initial_membership=104&name=Unauthorized+console+creation",
		"application/x-www-form-urlencoded"), 302)
	if !strings.HasPrefix(deniedAdmin.Header().Get("Location"), "/admin/login/") {
		t.Fatal("tenant administrator accepted at platform console")
	}
	var inactiveOutlet int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM outlets WHERE entity_id=104 AND name='Inactive branch'`).Scan(&inactiveOutlet); err != nil {
		t.Fatal(err)
	}
	check(runAs(initialOwner, "", "PUT", fmt.Sprintf("/api/v1/entities/104/outlets/%d", inactiveOutlet),
		`{"nombre":"Inactive branch","activa":true}`, "application/json"), 200)
	if got := check(runAs(nil, key.Token, "GET", "/api/v1/mcp/entities/104/outlets", "", ""), 200).Body.String(); !strings.Contains(got, "Inactive branch") {
		t.Fatal("initial grant did not let its tenant administrator activate the outlet")
	}
	check(runAs(nil, key.Token, "GET", "/api/v1/mcp/entities/101/outlets", "", ""), 404)
	var csrfOnly []*http.Cookie
	for _, cookie := range initialOwner {
		if cookie.Name == "csrftoken" {
			csrfOnly = append(csrfOnly, cookie)
		}
	}
	check(runAs(csrfOnly, key.Token, "POST", "/api/v1/entities/104/outlets", `{"nombre":"MCP attempt"}`, "application/json"), 401)
	check(postForm(adminLegalEntityAddPath, url.Values{
		"name": {"Catalog test entity"}, "cuit": {"00000000027"}, "active": {"on"},
	}), 302)
	var catalogSupplierID int64
	if err := db.QueryRowContext(ctx, "SELECT id FROM suppliers WHERE entity_id=101 AND cuit='00000000019'").Scan(&catalogSupplierID); err != nil {
		t.Fatal(err)
	}
	check(postForm(adminSupplierPath+fmt.Sprintf("%d/change/", catalogSupplierID), url.Values{
		"business_name": {"Renamed catalog test supplier"},
	}), 302)
	var history []string
	rows, err := db.QueryContext(ctx, "SELECT action FROM admin_supplier_history WHERE supplier_id=$1 ORDER BY id", catalogSupplierID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		history = append(history, action)
	}
	if rows.Err() != nil || strings.Join(history, ",") != "add,change" {
		t.Fatalf("admin supplier history: %v, %v", history, rows.Err())
	}
	check(run("GET", adminSupplierPath+fmt.Sprintf("%d/history/", catalogSupplierID), "", ""), 200)
	var documentID int64
	err = db.QueryRowContext(ctx, `INSERT INTO documents(entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,
		point_of_sale,number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at)
		VALUES (101,101,$1,'00000000000','invoice','A',1,9,'2026-01-01','ARS',100,'Fixture','draft',0,
		'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z') RETURNING id`, item.ID).Scan(&documentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO document_audit(document_id,actor_id,action,occurred_at,reason) VALUES
		($1,101,'update','2026-01-01T00:00:00.123456799Z','later fixture'),
		($1,101,'create','2026-01-01T03:00:00.123456701+03:00','earlier fixture')`, documentID); err != nil {
		t.Fatal(err)
	}
	auditHTML := check(run("GET", documentAuditAdminPath, "", ""), 200).Body.String()
	early, late := strings.Index(auditHTML, "earlier fixture"), strings.Index(auditHTML, "later fixture")
	if early < 0 || late < 0 || early >= late {
		t.Fatal("expected audit rows in UTC nanosecond order")
	}
	oldCookie := cookies["sessionid"]
	const replacementPassword = "  Local?Unit$Revised-b2kF8  "
	passwordForm := url.Values{
		"old_password": {password}, "new_password1": {replacementPassword}, "new_password2": {replacementPassword},
	}
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION gomar.refuse_test_session_change() RETURNS trigger
		LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic session refusal'; END;$$;
		CREATE TRIGGER refuse_test_session_change BEFORE UPDATE ON gomar.sessions
		FOR EACH ROW EXECUTE FUNCTION gomar.refuse_test_session_change()`); err != nil {
		t.Fatal(err)
	}
	check(postForm(adminPasswordPath, passwordForm), 500)
	check(run("GET", "/api/v1/auth/me", "", ""), 200)
	var unchanged string
	if err := db.QueryRowContext(ctx, "SELECT password FROM users WHERE id=101").Scan(&unchanged); err != nil || unchanged != hash {
		t.Fatalf("failed session change must preserve the existing password: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER refuse_test_session_change ON gomar.sessions;
		DROP FUNCTION gomar.refuse_test_session_change()`); err != nil {
		t.Fatal(err)
	}
	rotation := check(postForm(adminPasswordPath, passwordForm), 302)
	if rotation.Header().Get("Location") != adminPasswordDone {
		t.Fatalf("password rotation redirect = %q", rotation.Header().Get("Location"))
	}
	check(run("GET", "/api/v1/auth/me", "", ""), 401)
	for _, cookie := range rotation.Result().Cookies() {
		if cookie.Name == "sessionid" {
			cookies[cookie.Name] = cookie
		}
	}
	if cookies["sessionid"] == nil || oldCookie == nil || cookies["sessionid"].Value == oldCookie.Value {
		t.Fatal("password rotation did not replace the session")
	}
	check(run("GET", adminPasswordDone, "", ""), 200)
}
