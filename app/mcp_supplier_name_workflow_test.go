package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPSupplierNameWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-edit-workflow-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'edit-workflow',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'purchasing',TRUE);
 INSERT INTO outlet_grants VALUES(1,1);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(88001,1,'Fixture supplier','00000000019',TRUE)`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true, enableMCPSupplierNameProposals: true}
	h := a.routes()
	cookies := postgresTestCookies(t, h, "edit-workflow", password)
	otherLogin := postgresTestCookies(t, h, "edit-workflow", password)
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

	const api = "/api/v1/mcp/entities/1/outlets/1/supplier-legal-name-proposals"
	const browser = "/api/v1/entities/1/outlets/1/supplier-legal-name-proposals"
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["supplier_legal_name_change"]}`
	readToken := func(raw string) string {
		t.Helper()
		var result struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil || len(result.Token) != 43 {
			t.Fatal("synthetic key missing")
		}
		return result.Token
	}
	call("POST", proposalSessionsAuthPath, strings.Replace(issue, `"outlet_id":1`, `"outlet_id":2`, 1), "", cookies, 404)
	token := readToken(call("POST", proposalSessionsAuthPath, issue, "", cookies, 201).Body.String())
	wrongLoginToken := readToken(call("POST", proposalSessionsAuthPath, issue, "", otherLogin, 201).Body.String())
	read := readToken(call("POST", "/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, "", cookies, 201).Body.String())
	const one = `{"request_id":"55555555-0000-4000-8000-000000000001","supplier":{"supplier_id":88001,"expected_revision":1,"name":"Fixture renamed"}}`
	call("POST", api, one, read, nil, 401)
	call("POST", api, one, token, cookies, 422)
	call("POST", api, strings.Replace(one, `,"name":"Fixture renamed"`, "", 1), token, nil, 422)
	check := func(revision, approved, audited int, name string) {
		t.Helper()
		var rev, approvedCount, auditCount int
		var got string
		var unchangedID bool
		err := db.QueryRowContext(ctx, `SELECT revision,name,cuit='00000000019',
 (SELECT count(*) FROM mcp_supplier_legal_name_proposals WHERE status='approved'),
 (SELECT count(*) FROM mcp_supplier_legal_name_audit WHERE supplier_id=88001 AND authorization_outlet_id=1 AND entity_id=1 AND actor_id=1)
 FROM suppliers WHERE id=88001 AND entity_id=1 AND active`).Scan(&rev, &got, &unchangedID, &approvedCount, &auditCount)
		if err != nil || rev != revision || got != name || !unchangedID || approvedCount != approved || auditCount != audited {
			t.Fatalf("unexpected supplier state revision=%d approvals=%d audit=%d", rev, approvedCount, auditCount)
		}
	}
	queue := func(raw, name string) (string, string) {
		t.Helper()
		first := call("POST", api, raw, token, nil, 201)
		retry := call("POST", api, raw, token, nil, 200)
		if first.Body.String() != retry.Body.String() || strings.Contains(first.Body.String(), "supplier") {
			t.Fatal("unbounded bearer receipt")
		}
		var receipt supplierNameWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID < 1 {
			t.Fatal("invalid proposal receipt")
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		details := call("GET", path, "", "", cookies, 200)
		var preview supplierNameWireDetails
		if err := json.Unmarshal(details.Body.Bytes(), &preview); err != nil || preview.Supplier == nil ||
			preview.Supplier.Name != name || preview.Supplier.CurrentName != "Fixture supplier" || preview.Supplier.Revision != 1 ||
			!preview.Supplier.BusinessWide || len(preview.Digest) != 64 || strings.Contains(details.Body.String(), "00000000019") {
			t.Fatal("invalid supplier preview")
		}
		return path, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, preview.Digest)
	}
	first, d1 := queue(one, "Fixture renamed")
	two := strings.Replace(strings.Replace(one, "000000000001", "000000000002", 1), "Fixture renamed", "Second proposed name", 1)
	second, d2 := queue(two, "Second proposed name")
	call("POST", api, one, wrongLoginToken, nil, 409)
	check(1, 0, 0, "Fixture supplier")
	call("POST", first+"/decision", d1, "", otherLogin, 404)
	call("POST", first+"/decision", strings.Replace(d1, "true", "false", 1), "", cookies, 422)
	approval := call("POST", first+"/decision", d1, "", cookies, 200)
	var receipt supplierNameWireDetails
	if err := json.Unmarshal(approval.Body.Bytes(), &receipt); err != nil || receipt.SupplierRevision == nil || *receipt.SupplierRevision != 2 ||
		receipt.SupplierID == nil || *receipt.SupplierID != 88001 || receipt.Supplier != nil {
		t.Fatal("invalid browser receipt")
	}
	call("POST", first+"/decision", d1, "", cookies, 409)
	call("POST", second+"/decision", d2, "", cookies, 409)
	retry := call("POST", api, one, token, nil, 200)
	if strings.Contains(retry.Body.String(), "supplier") {
		t.Fatal("terminal bearer not redacted")
	}
	check(2, 1, 1, "Fixture renamed")
	if _, err := db.ExecContext(ctx, `DELETE FROM mcp_supplier_legal_name_proposals WHERE status='approved'`); err != nil {
		t.Fatal("could not rehearse expired receipt retention")
	}
	check(2, 0, 1, "Fixture renamed")
}
