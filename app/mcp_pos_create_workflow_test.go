package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPPOSCreateWorkflow(t *testing.T) {
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
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(88001,1,1,'Fixture',200,TRUE)`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableMCPPOSCreateProposals: true}
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

	const api = "/api/v1/mcp/entities/1/outlets/1/pos-create-proposals"
	const browser = "/api/v1/entities/1/outlets/1/pos-create-proposals"
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["pos_product_create"]}`
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
	const one = `{"request_id":"55555555-0000-4000-8000-000000000001","product":{"name":" Café ","price_minor":120,"active":false}}`
	call("POST", api, one, read, nil, 401)
	call("POST", api, one, token, cookies, 422)
	call("POST", api, strings.Replace(one, `,"active":false`, "", 1), token, nil, 422)
	assertRows := func(expected int) {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pos_products WHERE outlet_id=1`).Scan(&count); err != nil || count != expected {
			t.Fatal(count, err)
		}
	}
	submit := func(raw string) string {
		t.Helper()
		first := call("POST", api, raw, token, nil, 201)
		retry := call("POST", api, raw, token, nil, 200)
		if first.Body.String() != retry.Body.String() || strings.Contains(first.Body.String(), "product") {
			t.Fatal("unbounded receipt")
		}
		var p posCreateWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &p); err != nil || p.ID <= 0 || p.Status != "pending" {
			t.Fatal(p, err)
		}
		return fmt.Sprintf("%s/%d", browser, p.ID)
	}
	review := func(path string, count int) string {
		t.Helper()
		var p posCreateWireDetails
		if err := json.Unmarshal(call("GET", path, "", "", cookies, 200).Body.Bytes(), &p); err != nil || p.Product == nil || p.Product.Name != "Café" ||
			p.Product.Active || p.Product.Price != 120 || p.Product.Currency != "ARS" || p.Product.Existing != count || p.Product.Limit != 500 || len(p.Digest) != 64 {
			t.Fatal(p, err)
		}
		return fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, p.Digest)
	}
	ap := submit(one)
	call("GET", browser, "", "", cookies, 200)
	call("GET", ap, "", "", otherLogin, 404)
	call("POST", api, one, wrongLoginToken, nil, 409)
	call("POST", api, strings.Replace(one, `"price_minor":120`, `"price_minor":140`, 1), token, nil, 409)
	const secondSuffix = "000000000002"
	bp := submit(strings.Replace(one, "000000000001", secondSuffix, 1))
	ad, bd := review(ap, 1), review(bp, 1)
	assertRows(1)
	call("POST", ap+"/decision", ad, "", otherLogin, 404)
	call("POST", ap+"/decision", strings.Replace(ad, `"confirm":true`, `"confirm":false`, 1), "", cookies, 422)
	call("POST", ap+"/decision", ad, "", cookies, 200)
	call("POST", ap+"/decision", ad, "", cookies, 409)
	call("POST", bp+"/decision", bd, "", cookies, 409)
	approvedRetry := call("POST", api, one, token, nil, 200).Body.String()
	if strings.Contains(approvedRetry, "product") || !strings.Contains(approvedRetry, `"approved"`) {
		t.Fatal("unbounded approved replay")
	}
	assertRows(2)
	var approved posCreateWireDetails
	if err := json.Unmarshal(call("GET", ap, "", "", cookies, 200).Body.Bytes(), &approved); err != nil || approved.ProductID == nil || approved.ProductRevision == nil || *approved.ProductRevision != 1 || approved.Product != nil || approved.Digest != "" {
		t.Fatal(approved, err)
	}
	call("POST", bp+"/decision", strings.Replace(bd, `"approve"`, `"reject"`, 1), "", cookies, 200)
	var name string
	var price, revision int
	var active bool
	if err := db.QueryRowContext(ctx, `SELECT v.name,v.price_minor,v.revision,v.active FROM pos_product_versions v WHERE v.product_id=$1 AND v.outlet_id=1`, *approved.ProductID).Scan(&name, &price, &revision, &active); err != nil || name != "Café" || price != 120 || revision != 1 || active {
		t.Fatal(name, price, revision, active, err)
	}
	assertRows(2)
}
