package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPReconciliationDraftWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-reconciliation-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'reconciliation-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCPReconciliationDraftProposals: true}
	handler := a.routes()
	cookies := postgresTestCookies(t, handler, "reconciliation-fixture", password)
	anotherLogin := postgresTestCookies(t, handler, "reconciliation-fixture", password)
	call := func(method, path, body, token string, login []*http.Cookie, expected int) *httptest.ResponseRecorder {
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
		if w.Code != expected {
			t.Fatalf("%s %s status=%d want=%d: %s", method, path, w.Code, expected, w.Body)
		}
		return w
	}
	const browser = "/api/v1/entities/1/outlets/1/reconciliation-draft-proposals"
	const mcp = "/api/v1/mcp/entities/1/outlets/1/reconciliation-draft-proposals"
	check := func(want int) {
		t.Helper()
		var runs, links, audits, marker, sources int
		err := db.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM reconciliation_read_runs WHERE entity_id=1 AND revision=1 AND state='draft' AND NOT match_reference),
 (SELECT count(*) FROM reconciliation_read_outlets WHERE outlet_id=1),
 (SELECT count(*) FROM reconciliation_detail_events WHERE ordinal=1 AND actor_id=1),
 (SELECT count(*) FROM reconciliation_native_v1_runs),
 (SELECT count(*) FROM reconciliation_read_sources)`).Scan(&runs, &links, &audits, &marker, &sources)
		if err != nil || runs != want || links != want || audits != want || marker != want || sources != 0 {
			t.Fatal(runs, links, audits, marker, sources, err)
		}
	}
	loginBody := `{"entity_id":1,"outlet_id":1,"actions":["reconciliation_draft_create"]}`
	call("POST", proposalSessionsAuthPath, strings.Replace(loginBody, `"outlet_id":1`, `"outlet_id":2`, 1), "", cookies, 404)
	minted := call("POST", proposalSessionsAuthPath, loginBody, "", cookies, 201)
	var token struct {
		ID    int64  `json:"id"`
		Value string `json:"token"`
	}
	if err := json.Unmarshal(minted.Body.Bytes(), &token); err != nil || len(token.Value) != 43 {
		t.Fatal("missing synthetic session")
	}
	body := `{"request_id":"44444444-0000-4000-8000-000000000001","draft":{"match_reference":false}}`
	call("POST", mcp, strings.Replace(body, "false", "null", 1), token.Value, nil, 422)
	call("POST", mcp, strings.Replace(body, `"match_reference":false`, "", 1), token.Value, nil, 422)
	first := call("POST", mcp, body, token.Value, nil, 201)
	replay := call("POST", mcp, body, token.Value, nil, 200)
	if first.Body.String() != replay.Body.String() {
		t.Fatal("pending receipt changed on exact replay")
	}
	call("POST", mcp, strings.Replace(body, "false", "true", 1), token.Value, nil, 409)
	var receipt reconciliationDraftWireReceipt
	if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.RunID != nil || receipt.Status != "pending" {
		t.Fatal(receipt, err)
	}
	check(0)
	path := fmt.Sprintf("%s/%d", browser, receipt.ID)
	call("GET", path, "", "", anotherLogin, 404)
	var preview reconciliationDraftWireDetails
	if err := json.Unmarshal(call("GET", path, "", "", cookies, 200).Body.Bytes(), &preview); err != nil || preview.Draft == nil || preview.Draft.MatchReference || preview.RunID != nil || len(preview.Digest) != 64 {
		t.Fatal(preview, err)
	}
	decision := fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, preview.Digest)
	approved := call("POST", path+"/decision", decision, "", cookies, 200)
	call("POST", path+"/decision", decision, "", cookies, 409)
	if err := json.Unmarshal(approved.Body.Bytes(), &receipt); err != nil || receipt.RunID == nil || receipt.Status != "approved" {
		t.Fatal(receipt, err)
	}
	replay = call("POST", mcp, body, token.Value, nil, 200)
	if strings.Contains(replay.Body.String(), "run_id") {
		t.Fatal("bearer receipt exposed run identity")
	}
	check(1)
	body = strings.Replace(body, "000000000001", "000000000002", 1)
	second := call("POST", mcp, body, token.Value, nil, 201)
	if err := json.Unmarshal(second.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	call("DELETE", fmt.Sprintf("%s/%d", proposalSessionsAuthPath, token.ID), "", "", cookies, 204)
	terminal := call("GET", fmt.Sprintf("%s/%d", browser, receipt.ID), "", "", cookies, 200)
	if !strings.Contains(terminal.Body.String(), `"status":"revoked"`) || strings.Contains(terminal.Body.String(), `"draft"`) {
		t.Fatal("revoked proposal retained a preview")
	}
	check(1)
}
