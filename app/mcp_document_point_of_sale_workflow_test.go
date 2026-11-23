package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPDocumentPointOfSaleWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-prefix-workflow-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'point-buyer',$1,TRUE,FALSE,FALSE),(2,'point-admin',$1,TRUE,FALSE,FALSE),(3,'point-reader',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE),(2,'Other company','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Test A',TRUE),(2,1,'Other branch',TRUE),(3,2,'Elsewhere',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active)
 VALUES(1,1,1,'purchasing',TRUE),(2,2,1,'administration',TRUE),(3,3,1,'read_only',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(2,1),(2,2),(3,1);
 INSERT INTO suppliers(id,entity_id,name,cuit,active)
 VALUES(1,1,'Synthetic issuer','30711111111',TRUE),(2,2,'Other company issuer','30711111111',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,due_date,currency,subtotal_minor,tax_minor,total_minor,description,status,revision,created_at,updated_at)
 VALUES(1,1,1,1,'30711111111','invoice','A',1,8,'2026-11-22','2026-12-01','ARS',900,99,999,'Private note','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z'),
 (2,1,2,1,'30711111111','invoice','A',9,8,'2026-11-22',NULL,'ARS',NULL,NULL,300,'Other branch note','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z'),
 (3,2,3,2,'30711111111','invoice','A',17,8,'2026-11-22',NULL,'ARS',NULL,NULL,300,'Other company note','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z'),
 (4,1,1,1,'30711111111','invoice','A',0,8,'2026-11-22',NULL,'ARS',NULL,NULL,300,'Legacy prefix','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z'),
 (5,1,1,1,'30711111111','invoice','A',7,8,'2026-11-22',NULL,'ARS',NULL,NULL,300,'Deleted identity','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z');
 UPDATE documents SET deleted_at='2026-11-23T12:00:00Z' WHERE id=5;
 INSERT INTO document_attachments(document_id,storage_name,original_filename,mime_type,size,created_by_id,created_at)
 VALUES(1,'document-00000000000000000000000000000001','private-fixture.png','image/png',42,1,'2026-11-22T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCPDocumentPointOfSaleProposals: true, enableMCPDocumentNumberProposals: true}
	h := a.routes()
	buyer := postgresTestCookies(t, h, "point-buyer", password)
	otherLogin := postgresTestCookies(t, h, "point-buyer", password)
	admin := postgresTestCookies(t, h, "point-admin", password)
	reader := postgresTestCookies(t, h, "point-reader", password)
	call := func(handler http.Handler, method, path, body, token string, login []*http.Cookie, want int) *httptest.ResponseRecorder {
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
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	const browser = "/api/v1/entities/1/outlets/1/document-point-of-sale-proposals"
	const mcp = "/api/v1/mcp/entities/1/outlets/1/document-point-of-sale-proposals"
	const loginBody = `{"entity_id":1,"outlet_id":1,"actions":["document_draft_point_of_sale_change"]}`
	call((&app{db: db, host: a.host}).routes(), "GET", browser, "", "", buyer, 404)
	call(h, "POST", proposalSessionsAuthPath, loginBody, "", reader, 403)
	call(h, "POST", proposalSessionsAuthPath, strings.Replace(loginBody, `"outlet_id":1`, `"outlet_id":2`, 1), "", buyer, 404)
	issue := func(action string, cookies []*http.Cookie) string {
		t.Helper()
		var token struct {
			Value string `json:"token"`
		}
		if err := json.Unmarshal(call(h, "POST", proposalSessionsAuthPath, action, "", cookies, 201).Body.Bytes(), &token); err != nil || len(token.Value) != 43 {
			t.Fatal("missing synthetic session", err)
		}
		return token.Value
	}
	token := issue(loginBody, buyer)
	altToken := issue(loginBody, otherLogin)
	_ = issue(loginBody, admin)
	body := func(key, id, rev, point int) string {
		return fmt.Sprintf(`{"request_id":"55555555-0000-4000-8000-%012d","document_id":%d,"expected_revision":%d,"point_of_sale":%d}`, key, id, rev, point)
	}
	// Both consent lanes and public contracts coexist without cross-use.
	const legacyAPI = "/api/v1/mcp/entities/1/outlets/1/document-number-proposals"
	const legacyBrowser = "/api/v1/entities/1/outlets/1/document-number-proposals"
	legacyKey := issue(strings.Replace(loginBody, "document_draft_point_of_sale_change", "document_draft_number_change", 1), buyer)
	legacyBody := strings.Replace(body(2, 1, 1, 11), `"point_of_sale":`, `"number":`, 1)
	legacyPending := call(h, "POST", legacyAPI, legacyBody, legacyKey, nil, 201).Body.String()
	call(h, "POST", mcp, body(2, 1, 1, 17), legacyKey, nil, 401)
	call(h, "POST", legacyAPI, legacyBody, token, nil, 401)
	call(h, "POST", legacyAPI, body(2, 1, 1, 17), legacyKey, nil, 422)
	for _, invalid := range []string{body(1, 1, 0, 2), body(1, 1, 1, 0), body(1, 1, 1, 100000),
		strings.Replace(body(1, 1, 1, 17), `"point_of_sale":17`, `"point_of_sale":null`, 1),
		strings.Replace(body(1, 1, 1, 17), `"point_of_sale":17`, `"point_of_sale":17,"number":2`, 1)} {
		call(h, "POST", mcp, invalid, token, nil, 422)
	}
	call(h, "POST", mcp, body(1, 4, 1, 15), token, nil, 409)

	call(h, "POST", mcp, body(1, 2, 1, 17), token, nil, 404)
	call(h, "POST", strings.Replace(mcp, "/outlets/1/", "/outlets/2/", 1), body(1, 2, 1, 17), token, nil, 404)
	immutable := func() string {
		t.Helper()
		var got string
		err := db.QueryRowContext(ctx, `SELECT jsonb_build_array(to_jsonb(d)-'point_of_sale'-'revision'-'updated_at'-'updated_by_id',
		 (SELECT to_jsonb(a) FROM document_attachments a WHERE a.document_id=d.id),
		 (SELECT count(*) FROM ap_payables), (SELECT count(*) FROM ap_ledger), (SELECT count(*) FROM ap_financial_events))::text
		 FROM documents d WHERE id=1`).Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := immutable()
	check := func(point, revision, changes int) {
		t.Helper()
		var n, r, audit int
		err := db.QueryRowContext(ctx, `SELECT point_of_sale,revision,(SELECT count(*) FROM document_audit a
		 WHERE a.document_id=1 AND a.action='update' AND a.actor_id=1 AND a.reason=$1)
		 FROM documents WHERE id=1`, documentPointOfSaleAuditReason).Scan(&n, &r, &audit)
		if err != nil || n != point || r != revision || audit != changes {
			t.Fatalf("prefix/revision/audit=%d/%d/%d, err=%v", n, r, audit, err)
		}
	}
	submit := func(raw string) (string, *httptest.ResponseRecorder) {
		t.Helper()
		res := call(h, "POST", mcp, raw, token, nil, 201)
		var fields map[string]json.RawMessage
		var receipt documentPointOfSaleWireReceipt
		if err := json.Unmarshal(res.Body.Bytes(), &fields); err != nil || len(fields) != 3 {
			t.Fatal(res.Body, err)
		}
		if err := json.Unmarshal(res.Body.Bytes(), &receipt); err != nil || receipt.Status != "pending" || receipt.ID <= 0 {
			t.Fatal(receipt, err)
		}
		return fmt.Sprintf("%s/%d", browser, receipt.ID), res
	}
	preview := func(path, old, next string, revision int64) documentPointOfSaleWireDetails {
		t.Helper()
		var shown documentPointOfSaleWireDetails
		res := call(h, "GET", path, "", "", buyer, 200)
		if err := json.Unmarshal(res.Body.Bytes(), &shown); err != nil || shown.PointOfSaleChange == nil ||
			*shown.PointOfSaleChange != (documentPointOfSaleWirePreview{1, revision, old, next}) || len(shown.Digest) != 64 {
			t.Fatal(shown, err)
		}
		for _, s := range []string{"30711111111", "Synthetic issuer", "private-fixture.png", "Private note", "Other branch"} {
			if strings.Contains(res.Body.String(), s) {
				t.Fatal("unexpected preview data")
			}
		}
		return shown
	}
	decision := func(action, digest string, yes bool) string {
		return fmt.Sprintf(`{"decision":%q,"digest":%q,"confirm":%t}`, action, digest, yes)
	}

	// A conflicting prefix in an unauthorized branch looks like any other
	// pending proposal on the bearer channel. Its exact native constraint
	// failure is redacted even to the issuing browser.
	collision, _ := submit(body(1, 1, 1, 9))
	shown := preview(collision, "Factura A 00001-00000008", "Factura A 00009-00000008", 1)
	blocked := call(h, "POST", collision+"/decision", decision("approve", shown.Digest, true), "", buyer, 409)
	for _, s := range []string{"Other branch", "30711111111", "supplier", "documents_identity_unique"} {
		if strings.Contains(blocked.Body.String(), s) {
			t.Fatal("duplicate divulged another row", blocked.Body)
		}
	}
	check(1, 1, 0)

	deleted, _ := submit(body(12, 1, 1, 7))
	shown = preview(deleted, "Factura A 00001-00000008", "Factura A 00007-00000008", 1)
	deletedBlocked := call(h, "POST", deleted+"/decision", decision("approve", shown.Digest, true), "", buyer, 409)
	if deletedBlocked.Body.String() != blocked.Body.String() {
		t.Fatal("duplicate conflicts disclosed where the identity lives")
	}
	check(1, 1, 0)

	// The same printed identity can be used in a different business. Only
	// the issuing browser can act, even compared with another admin/login.
	raw := body(2, 1, 1, 17)
	path, created := submit(raw)
	retry := call(h, "POST", mcp, raw, token, nil, 200)
	if retry.Body.String() != created.Body.String() {
		t.Fatal("pending retry differs")
	}
	call(h, "POST", mcp, raw, altToken, nil, 409)
	call(h, "GET", path, "", "", otherLogin, 404)
	call(h, "GET", path, "", "", admin, 404)
	shown = preview(path, "Factura A 00001-00000008", "Factura A 00017-00000008", 1)
	call(h, "POST", path+"/decision", decision("approve", shown.Digest, false), "", buyer, 422)
	call(h, "POST", path+"/decision", decision("approve", shown.Digest, true), "", otherLogin, 404)
	check(1, 1, 0)
	approved := call(h, "POST", path+"/decision", decision("approve", shown.Digest, true), "", buyer, 200)
	var receipt documentPointOfSaleWireReceipt
	if err := json.Unmarshal(approved.Body.Bytes(), &receipt); err != nil || receipt.Status != "approved" ||
		receipt.DocumentID == nil || *receipt.DocumentID != 1 || receipt.DocumentRevision == nil || *receipt.DocumentRevision != 2 {
		t.Fatal(receipt, err)
	}
	call(h, "POST", path+"/decision", decision("approve", shown.Digest, true), "", buyer, 409)
	retry = call(h, "POST", mcp, raw, token, nil, 200)
	if strings.Contains(retry.Body.String(), "document") || !strings.Contains(retry.Body.String(), "approved") {
		t.Fatal("nonredacted or stale bearer receipt")
	}
	check(17, 2, 1)
	if replayLegacy := call(h, "POST", legacyAPI, legacyBody, legacyKey, nil, 200).Body.String(); replayLegacy != legacyPending {
		t.Fatal("prefix lane changed number-only pending digest/receipt")
	}
	var legacyList struct {
		Proposals []documentNumberWireReceipt `json:"proposals"`
	}
	if err := json.Unmarshal(call(h, "GET", legacyBrowser, "", "", buyer, 200).Body.Bytes(), &legacyList); err != nil || len(legacyList.Proposals) != 1 {
		t.Fatal("number browser exposed new proposal rows", err)
	}
	same, _ := submit(body(10, 1, 2, 17))
	sameView := preview(same, "Factura A 00017-00000008", "Factura A 00017-00000008", 2)
	call(h, "POST", same+"/decision", decision("approve", sameView.Digest, true), "", buyer, 409)
	call(h, "POST", same+"/decision", decision("reject", sameView.Digest, true), "", buyer, 200)
	if immutable() != before {
		t.Fatal("correction touched another field, attachment, or finance record")
	}

	rejected, _ := submit(body(3, 1, 2, 18))
	shown = preview(rejected, "Factura A 00017-00000008", "Factura A 00018-00000008", 2)
	res := call(h, "POST", rejected+"/decision", decision("reject", shown.Digest, true), "", buyer, 200)
	if strings.Contains(res.Body.String(), "document") || !strings.Contains(res.Body.String(), "rejected") {
		t.Fatal("rejection has nonredacted receipt")
	}
	check(17, 2, 1)

	stale, _ := submit(body(4, 1, 2, 18))
	shown = preview(stale, "Factura A 00017-00000008", "Factura A 00018-00000008", 2)
	const native = "/api/v1/entities/1/documents/1"
	call(h, "PUT", native, `{"outlet_id":1,"supplier_id":1,"issuer_cuit":"30711111111","tipo":"invoice","letra":"A","punto_venta":19,"numero":8,"fecha_emision":"2026-11-22","fecha_vencimiento":"2026-12-01","moneda":"ARS","neto":"9.00","iva":"0.99","total":"9.99","descripcion":"Private note","expected_revision":2,"motivo":"Native fixture edit"}`, "", buyer, 200)
	call(h, "POST", stale+"/decision", decision("approve", shown.Digest, true), "", buyer, 409)
	check(19, 3, 1)

	afterReview, _ := submit(body(5, 1, 3, 20))
	shown = preview(afterReview, "Factura A 00019-00000008", "Factura A 00020-00000008", 3)
	call(h, "POST", native+"/review", `{"expected_revision":3,"motivo":"Native fixture review"}`, "", admin, 200)
	call(h, "POST", afterReview+"/decision", decision("approve", shown.Digest, true), "", buyer, 409)
	call(h, "POST", mcp, body(6, 1, 4, 20), token, nil, 409)
	check(19, 4, 1)
}
