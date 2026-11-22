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
		{"INSERT INTO users(id,username,password,active,staff,superuser) VALUES (101,'admin-fixture',$1,TRUE,TRUE,TRUE),(102,'clerk-fixture',$1,TRUE,FALSE,FALSE)", []any{hash}},
		{"INSERT INTO entities(id,name,cuit,active) VALUES (101,'Tenant One','00000000000',TRUE),(102,'Tenant Two','00000000001',TRUE)", nil},
		{"INSERT INTO outlets(id,entity_id,name,active) VALUES (101,101,'Center',TRUE),(102,102,'Foreign',TRUE)", nil},
		{"INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES (101,101,101,'administration',TRUE)", nil},
		{"INSERT INTO outlet_grants(membership_id,outlet_id) VALUES (101,101)", nil},
	} {
		if _, err := db.ExecContext(ctx, seed.q, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{db: db, host: "example.com"}
	handler := a.routes()
	cookies := map[string]*http.Cookie{}
	run := func(method, path, body, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		if csrf := cookies["csrftoken"]; csrf != nil {
			r.Header.Set("X-CSRFToken", csrf.Value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
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
	api("POST", "/outlets", `{"nombre":"New outlet","activa":true}`, 201)
	api("POST", "/outlets", `{"nombre":"New outlet","activa":true}`, 409)
	api("PUT", "/outlets/102", `{"nombre":"Foreign edit","activa":false}`, 404)
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
	check(postForm(adminOutletAddPath, url.Values{
		"entity": {"101"}, "name": {"Catalog test outlet"}, "active": {"on"},
	}), 302)
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
