package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPDocumentEditWorkflow(t *testing.T) {
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
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Synthetic supplier','30711111111',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at)
 VALUES(1,1,1,1,'30711111111','invoice','A',1,8,'2026-11-22','ARS',999,'Untouched','draft',0,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCPDocumentEditProposals: true}
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
	const browser = "/api/v1/entities/1/outlets/1/document-edit-proposals"
	const mcp = "/api/v1/mcp/entities/1/outlets/1/document-edit-proposals"
	check := func(revision, total, updates int) {
		t.Helper()
		var rev, value, audit, payables int
		err := db.QueryRowContext(ctx, `SELECT revision,total_minor,
 (SELECT count(*) FROM document_audit WHERE document_id=1 AND actor_id=1 AND action='update' AND reason='Fecha corregida'),
 (SELECT count(*) FROM ap_payables WHERE source_document_id=1)
 FROM documents WHERE id=1 AND status='draft' AND outlet_id=1 AND supplier_id=1 AND currency='ARS' AND description='Untouched'`).Scan(&rev, &value, &audit, &payables)
		if err != nil || rev != revision || value != total || audit != updates || payables != 0 {
			t.Fatal(rev, value, audit, payables, err)
		}
	}
	loginBody := `{"entity_id":1,"outlet_id":1,"actions":["document_draft_edit"]}`
	call("POST", proposalSessionsAuthPath, strings.Replace(loginBody, `"outlet_id":1`, `"outlet_id":2`, 1), "", cookies, 404)
	var token struct {
		Value string `json:"token"`
	}
	if err := json.Unmarshal(call("POST", proposalSessionsAuthPath, loginBody, "", cookies, 201).Body.Bytes(), &token); err != nil || len(token.Value) != 43 {
		t.Fatal("missing synthetic session")
	}
	body := `{"request_id":"55555555-0000-4000-8000-000000000001","edit":` + documentEditFixtureJSON + `}`
	created := call("POST", mcp, body, token.Value, nil, 201)
	replay := call("POST", mcp, body, token.Value, nil, 200)
	if created.Body.String() != replay.Body.String() || strings.Contains(created.Body.String(), "document_id") {
		t.Fatal("pending receipt mismatch")
	}
	conflicting := strings.Replace(body, "Fecha corregida", "Motivo distinto", 1)
	call("POST", mcp, conflicting, token.Value, nil, 409)
	var receipt documentEditWireReceipt
	if err := json.Unmarshal(created.Body.Bytes(), &receipt); err != nil || receipt.Status != "pending" {
		t.Fatal(receipt, err)
	}
	check(0, 999, 0)
	path := fmt.Sprintf("%s/%d", browser, receipt.ID)
	call("GET", path, "", "", otherLogin, 404)
	var preview documentEditWireDetails
	if err := json.Unmarshal(call("GET", path, "", "", cookies, 200).Body.Bytes(), &preview); err != nil || preview.Edit == nil ||
		preview.Edit.Before.Total != 999 || preview.Edit.After.Total != 1210 || preview.Edit.Revision != 0 || preview.Edit.Currency != "ARS" {
		t.Fatal(preview, err)
	}
	decision := fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, preview.Digest)
	call("POST", path+"/decision", decision, "", otherLogin, 404)
	approved := call("POST", path+"/decision", decision, "", cookies, 200)
	call("POST", path+"/decision", decision, "", cookies, 409)
	if err := json.Unmarshal(approved.Body.Bytes(), &receipt); err != nil || receipt.DocumentRevision == nil || *receipt.DocumentRevision != 1 || receipt.Status != "approved" {
		t.Fatal(receipt, err)
	}
	replay = call("POST", mcp, body, token.Value, nil, 200)
	if strings.Contains(replay.Body.String(), "document_id") || strings.Contains(replay.Body.String(), "document_revision") {
		t.Fatal("bearer receipt exposed draft identity")
	}
	check(1, 1210, 1)
}
