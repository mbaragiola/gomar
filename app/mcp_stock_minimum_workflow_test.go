package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPStockMinimumWorkflow(t *testing.T) {
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
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1);
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(88001,1,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(88001,1,1,'Fixture',200,TRUE);
 INSERT INTO stock_outlets VALUES(1,TRUE);
 INSERT INTO stock_products VALUES(1,88001,TRUE,8)`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableStock: true, enableMCPStockMinimumProposals: true}
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

	const api = "/api/v1/mcp/entities/1/outlets/1/stock-minimum-proposals"
	const browser = "/api/v1/entities/1/outlets/1/stock-minimum-proposals"
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["stock_minimum_change"]}`
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
	const one = `{"request_id":"55555555-0000-4000-8000-000000000001","minimum_change":{"product_id":88001,"expected_revision":1,"minimum":0}}`
	call("POST", api, one, read, nil, 401)
	call("POST", api, one, token, cookies, 422)
	call("POST", api, strings.Replace(one, `,"minimum":0`, "", 1), token, nil, 422)
	check := func(minimum int, approved int) {
		t.Helper()
		var got, count, moves, sales, rev int
		var tracking, enabled bool
		err := db.QueryRowContext(ctx, `SELECT t.minimum,t.enabled,o.enabled,p.current_revision,
  (SELECT count(*) FROM mcp_stock_minimum_proposals WHERE status='approved'),
  (SELECT count(*) FROM stock_movements),(SELECT count(*) FROM pos_sales)
 FROM stock_products t JOIN stock_outlets o ON o.outlet_id=t.outlet_id
 JOIN pos_products p ON p.id=t.product_id AND p.outlet_id=t.outlet_id
 WHERE t.outlet_id=1 AND t.product_id=88001`).Scan(&got, &tracking, &enabled, &rev, &count, &moves, &sales)
		if err != nil || got != minimum || count != approved || moves != 0 || sales != 0 || rev != 1 || !tracking || !enabled {
			t.Fatal(got, tracking, enabled, rev, count, moves, sales, err)
		}
	}
	queue := func(raw string, want int) (string, string) {
		t.Helper()
		first := call("POST", api, raw, token, nil, 201)
		retry := call("POST", api, raw, token, nil, 200)
		if first.Body.String() != retry.Body.String() || strings.Contains(first.Body.String(), "minimum") || strings.Contains(first.Body.String(), "product_id") {
			t.Fatal("unbounded receipt")
		}
		var receipt stockMinimumWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID < 1 {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		var preview stockMinimumWireDetails
		if err := json.Unmarshal(call("GET", path, "", "", cookies, 200).Body.Bytes(), &preview); err != nil ||
			preview.MinimumChange == nil || preview.MinimumChange.Name != "Fixture" || preview.MinimumChange.Previous != 8 ||
			preview.MinimumChange.Minimum != int64(want) || preview.MinimumChange.Revision != 1 || len(preview.Digest) != 64 {
			t.Fatal(preview, err)
		}
		return path, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, preview.Digest)
	}
	first, d1 := queue(one, 0)
	two := strings.Replace(strings.Replace(one, "000000000001", "000000000002", 1), `"minimum":0`, `"minimum":10`, 1)
	second, d2 := queue(two, 10)
	call("POST", api, one, wrongLoginToken, nil, 409)
	check(8, 0)
	call("POST", first+"/decision", d1, "", otherLogin, 404)
	call("POST", first+"/decision", strings.Replace(d1, "true", "false", 1), "", cookies, 422)
	approval := call("POST", first+"/decision", d1, "", cookies, 200)
	var receipt stockMinimumWireDetails
	if err := json.Unmarshal(approval.Body.Bytes(), &receipt); err != nil || receipt.Minimum == nil || *receipt.Minimum != 0 || receipt.ProductID == nil || *receipt.ProductID != 88001 || receipt.MinimumChange != nil {
		t.Fatal(receipt, err)
	}
	call("POST", first+"/decision", d1, "", cookies, 409)
	call("POST", second+"/decision", d2, "", cookies, 409)
	retry := call("POST", api, one, token, nil, 200)
	if strings.Contains(retry.Body.String(), "minimum") || strings.Contains(retry.Body.String(), "product_id") {
		t.Fatal("terminal bearer not redacted")
	}
	check(0, 1)
}
