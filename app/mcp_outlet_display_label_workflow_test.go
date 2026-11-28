package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPOutletDisplayLabelWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-outlet-label-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'outlet-label-workflow',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE),(2,'Another fixture','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES
 (1,1,'Centro',TRUE),(2,1,'Norte',TRUE),(3,2,'Centro renovado',TRUE),(4,1,'Local inactive',FALSE),(5,1,'Sur',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,4);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'private-supplier','00000000003',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,
 currency,total_minor,description,status,revision,created_at,updated_at)
 VALUES(1,1,1,1,'00000000003','invoice','A',1,1,'2026-11-01','ARS',10000,'private-doc','reviewed',1,
 '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z');
 INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled,collect_contact)
 VALUES(1,'private-fixture-slug','Public name fixture','private-policy',TRUE,TRUE);
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(41,1,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(41,1,1,'POS fixture',200,TRUE);
 INSERT INTO stock_outlets VALUES(1,TRUE);
 INSERT INTO stock_products VALUES(1,41,TRUE,5);`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableStock: true, enableMCPOutletDisplayLabelProposals: true}
	h := a.routes()
	cookies := postgresTestCookies(t, h, "outlet-label-workflow", password)
	otherLogin := postgresTestCookies(t, h, "outlet-label-workflow", password)
	call := func(method, path, body, token string, login []*http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if login != nil {
			r.Header.Set("Origin", "http://example.com")
			for _, c := range login {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}

	const api = "/api/v1/mcp/entities/1/outlets/1/outlet-display-label-proposals"
	const browser = "/api/v1/entities/1/outlets/1/outlet-display-label-proposals"
	const native = "/api/v1/entities/1/outlets/1"
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["outlet_display_label_change"]}`
	off := app{db: db, host: "example.com", enableMCP: true}
	noRoute := httptest.NewRecorder()
	off.routes().ServeHTTP(noRoute, httptest.NewRequest("POST", "http://example.com"+api, nil))
	if noRoute.Code != 404 {
		t.Fatal("unexpected feature-on route")
	}
	key := func(path, input string, login []*http.Cookie) string {
		t.Helper()
		var data struct{ Token string }
		if err := json.Unmarshal(call("POST", path, input, "", login, 201).Body.Bytes(), &data); err != nil || len(data.Token) != 43 {
			t.Fatal("cannot issue synthetic key")
		}
		return data.Token
	}
	token := key(proposalSessionsAuthPath, issue, cookies)
	separate := key(proposalSessionsAuthPath, issue, otherLogin)
	read := key("/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, cookies)
	body := func(id int, old, next string) string {
		return fmt.Sprintf(`{"request_id":"55555555-0000-4000-8000-%012d","label_change":{"expected_name":%q,"name":%q}}`, id, old, next)
	}
	call("POST", api, body(1, "Centro", "Centro renovado"), read, nil, 401)
	call("POST", api, body(1, "Centro", "Centro renovado"), token, cookies, 422)
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/2/", 1), body(1, "Norte", "Otra"), token, nil, 404)
	call("POST", strings.Replace(api, "/entities/1/", "/entities/2/", 1), body(1, "Centro renovado", "Otra"), token, nil, 404)
	call("POST", proposalSessionsAuthPath, strings.Replace(issue, `"outlet_id":1`, `"outlet_id":4`, 1), "", cookies, 404)
	call("POST", proposalSessionsAuthPath, strings.Replace(issue, `"outlet_id":1`, `"outlet_id":5`, 1), "", cookies, 404)
	for _, invalid := range []string{
		body(1, "Centro", "Centro"), body(1, "Centro", " "), body(1, "Centro", " Centro nuevo"),
		body(1, "Centro", strings.Repeat("Á", 121)),
		strings.Replace(body(1, "Centro", "Nuevo"), `"Nuevo"`, `"Nuevo\u202e"`, 1),
		strings.Replace(body(1, "Centro", "Nuevo"), `"Nuevo"`, `"Nuevo","active":false`, 1),
		strings.Replace(body(1, "Centro", "Nuevo"), `"Nuevo"`, `null`, 1),
	} {
		call("POST", api, invalid, token, nil, 422)
	}
	call("POST", api, body(1, "Centro incorrecto", "Nuevo"), token, nil, 409)
	call("POST", api, body(1, "Centro", "Norte"), token, nil, 409)
	call("POST", api, body(1, "Centro", "Local inactive"), token, nil, 409)
	call("POST", "/api/v1/entities/1/documents/1/payable", `{"motivo":"synthetic private payable"}`, "", cookies, 201)
	const sale = `{"id_solicitud":"55555555-0000-4000-8000-000000000031","actor_id":1,"items":[{"producto_id":41,"revision":1,"cantidad":1}]}`
	sales := native + "/pos/sales"
	saleReceipt := call("POST", sales, sale, "", cookies, 201).Body.String()
	fingerprint := func() string {
		t.Helper()
		var digest string
		err := db.QueryRowContext(ctx, `SELECT md5(concat(
  (SELECT json_agg(x ORDER BY id)::text FROM entities x),
  (SELECT json_agg(x ORDER BY id)::text FROM memberships x),
  (SELECT json_agg(x ORDER BY membership_id,outlet_id)::text FROM outlet_grants x),
  (SELECT json_agg(x ORDER BY id)::text FROM documents x),
  (SELECT json_agg(x ORDER BY id)::text FROM ap_payables x),
  (SELECT json_agg(x ORDER BY id)::text FROM ap_ledger x),
  (SELECT json_agg(x ORDER BY id)::text FROM pos_sales x),
  (SELECT json_agg(x ORDER BY product_id,revision)::text FROM pos_product_versions x),
  (SELECT json_agg(x ORDER BY id)::text FROM stock_movements x),
  (SELECT json_agg(x ORDER BY outlet_id)::text FROM reservation_venues x)
 ))`).Scan(&digest)
		if err != nil || digest == "" {
			t.Fatal("cannot snapshot native outlet facts", err)
		}
		return digest
	}
	check := func(want string) {
		t.Helper()
		var name string
		var active bool
		var entity int64
		if err := db.QueryRowContext(ctx, `SELECT name,active,entity_id FROM outlets WHERE id=1`).Scan(&name, &active, &entity); err != nil ||
			name != want || !active || entity != 1 {
			t.Fatal(name, active, entity, err)
		}
	}
	type queued struct{ path, decision, digest string }
	queue := func(id int, old, next string) queued {
		t.Helper()
		raw := body(id, old, next)
		first := call("POST", api, raw, token, nil, 201)
		if retry := call("POST", api, raw, token, nil, 200).Body.String(); retry != first.Body.String() || strings.Contains(retry, "name") {
			t.Fatal("unbounded pending receipt")
		}
		var receipt outletDisplayLabelWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID <= 0 || receipt.Status != "pending" {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		var detail outletDisplayLabelWireDetails
		wire := call("GET", path, "", "", cookies, 200).Body.String()
		for _, secret := range []string{"private-", "cuit", "grant", "policy", "slug", "guest"} {
			if strings.Contains(wire, secret) {
				t.Fatal("private field appeared in preview", secret)
			}
		}
		if err := json.Unmarshal([]byte(wire), &detail); err != nil || detail.LabelChange == nil ||
			detail.LabelChange.CurrentName != old || detail.LabelChange.Name != next ||
			detail.LabelChange.OutletID != 1 || !detail.LabelChange.Active || len(detail.Digest) != 64 {
			t.Fatal(detail, err)
		}
		return queued{path, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, detail.Digest), detail.Digest}
	}
	before := fingerprint()
	first, stale := queue(10, "Centro", "Centro renovado"), queue(11, "Centro", "Centro alternativo")
	call("POST", api, body(10, "Centro", "Centro renovado"), separate, nil, 409)
	call("POST", first.path+"/decision", first.decision, "", otherLogin, 404)
	check("Centro")
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION gomar.qa_reject_mcp_catalog_audit() RETURNS trigger
		LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'synthetic catalog audit failure'; END;$$;
		CREATE TRIGGER qa_reject_mcp_catalog_audit BEFORE INSERT ON tenant_catalog_events
		FOR EACH ROW EXECUTE FUNCTION gomar.qa_reject_mcp_catalog_audit()`); err != nil {
		t.Fatal(err)
	}
	call("POST", first.path+"/decision", first.decision, "", cookies, 500)
	check("Centro")
	if _, err := db.ExecContext(ctx, `DROP TRIGGER qa_reject_mcp_catalog_audit ON tenant_catalog_events;
		DROP FUNCTION gomar.qa_reject_mcp_catalog_audit()`); err != nil {
		t.Fatal(err)
	}
	result := call("POST", first.path+"/decision", first.decision, "", cookies, 200)
	var receipt outletDisplayLabelWireDetails
	if err := json.Unmarshal(result.Body.Bytes(), &receipt); err != nil || receipt.LabelChange != nil || receipt.Name == nil || *receipt.Name != "Centro renovado" {
		t.Fatal(result.Body.String(), err)
	}
	if approved := call("GET", first.path, "", "", cookies, 200).Body.String(); approved != result.Body.String() {
		t.Fatal("stored receipt was not exact")
	}
	if replayed := call("POST", api, body(10, "Centro", "Centro renovado"), token, nil, 200).Body.String(); strings.Contains(replayed, "name") || !strings.Contains(replayed, "approved") {
		t.Fatal("terminal bearer exposed label fields")
	}
	call("POST", first.path+"/decision", first.decision, "", cookies, 409)
	call("POST", stale.path+"/decision", stale.decision, "", cookies, 409)
	check("Centro renovado")
	var auditCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tenant_catalog_events
		WHERE entity_id=1 AND outlet_id=1 AND actor_id=1 AND action='update_outlet'
		AND before='{"name":"Centro","active":true}'
		AND after='{"name":"Centro renovado","active":true}'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("approval did not record the expected catalog event: %d, %v", auditCount, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tenant_catalog_events`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("queue, denial, or retry wrote an extra event: %d, %v", auditCount, err)
	}
	if fingerprint() != before {
		t.Fatal("rename changed memberships, financial/sale records, or venue")
	}
	if got := call("POST", sales, sale, "", cookies, 200).Body.String(); got != saleReceipt {
		t.Fatal("accepted POS receipt changed")
	}
	if nativeRead := call("GET", "/api/v1/entities/1/payables/1", "", "", cookies, 200).Body.String(); !strings.Contains(nativeRead, "Centro renovado") {
		t.Fatal("joined label in prior payable did not reflect current display")
	}
	reverse := queue(12, "Centro renovado", "Centro")
	call("POST", reverse.path+"/decision", reverse.decision, "", cookies, 200)
	check("Centro")

	nativeStale := queue(13, "Centro", "Nombre distinto")
	call("PUT", native, `{"nombre":"Nombre manual","activa":true}`, "", cookies, 200)
	call("POST", nativeStale.path+"/decision", nativeStale.decision, "", cookies, 409)
	check("Nombre manual")
	duplicate := queue(14, "Nombre manual", "Tomado después")
	call("PUT", "/api/v1/entities/1/outlets/2", `{"nombre":"Tomado después","activa":true}`, "", cookies, 200)
	call("POST", duplicate.path+"/decision", duplicate.decision, "", cookies, 409)
	check("Nombre manual")
	reject := queue(15, "Nombre manual", "No aprobado")
	if out := call("POST", reject.path+"/decision", fmt.Sprintf(`{"decision":"reject","digest":%q,"confirm":true}`, reject.digest), "", cookies, 200).Body.String(); strings.Contains(out, "name") {
		t.Fatal("rejection leaked label receipt")
	}
	check("Nombre manual")
	// Two valid approvals for the same old name cannot both succeed.
	one := queue(17, "Nombre manual", "Resultado uno")
	two := queue(18, "Nombre manual", "Resultado dos")
	outcomes := make(chan int, 2)
	for _, proposal := range []queued{one, two} {
		go func(q queued) {
			req := httptest.NewRequestWithContext(ctx, "POST", "http://example.com"+q.path+"/decision", strings.NewReader(q.decision))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://example.com")
			for _, cookie := range cookies {
				req.AddCookie(cookie)
				if cookie.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", cookie.Value)
				}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			outcomes <- w.Code
		}(proposal)
	}
	x, y := <-outcomes, <-outcomes
	var name string
	var approvedCount int
	err := db.QueryRowContext(ctx, `SELECT o.name,(SELECT count(*) FROM mcp_outlet_display_label_proposals WHERE status='approved')
 FROM outlets o WHERE o.id=1`).Scan(&name, &approvedCount)
	if !(x == 200 && y == 409 || x == 409 && y == 200) || err != nil || approvedCount != 3 ||
		(name != "Resultado uno" && name != "Resultado dos") || fingerprint() != before {
		t.Fatal("competing decisions did not preserve exact named mutation", x, y, name, approvedCount, err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1"); err != nil {
		t.Fatal(err)
	}
	call("POST", api, body(16, "Nombre manual", "Perdido"), token, nil, 404)
	call("POST", duplicate.path+"/decision", duplicate.decision, "", cookies, 404)
	if _, err := db.ExecContext(ctx, "INSERT INTO outlet_grants VALUES(1,1); UPDATE memberships SET role='purchasing' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	call("POST", proposalSessionsAuthPath, issue, "", cookies, 403)
	call("POST", duplicate.path+"/decision", duplicate.decision, "", cookies, 403)
}
