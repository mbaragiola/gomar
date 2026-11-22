package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPPOSCatalogWorkflow(t *testing.T) {
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
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(1,1,1,'Fixture',200,TRUE)`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enablePOS: true, enableMCPPOSCatalogProposals: true}
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
	const browser = "/api/v1/entities/1/outlets/1/"
	const mcp = "/api/v1/mcp/entities/1/outlets/1/"
	mint := func(action string) string {
		t.Helper()
		body := fmt.Sprintf(`{"entity_id":1,"outlet_id":1,"actions":[%q]}`, action)
		var token struct {
			Value string `json:"token"`
		}
		if err := json.Unmarshal(call("POST", proposalSessionsAuthPath, body, "", cookies, 201).Body.Bytes(), &token); err != nil || len(token.Value) != 43 {
			t.Fatal("missing synthetic session")
		}
		return token.Value
	}
	pt := mint(posPriceSessionAction)
	at := mint(posAvailabilitySessionAction)
	check := func(revision, price int64, active bool) {
		t.Helper()
		var rev, cents, count int64
		var enabled bool
		var name string
		err := db.QueryRowContext(ctx, `SELECT p.current_revision,v.price_minor,v.active,v.name,
 (SELECT count(*) FROM pos_product_versions WHERE product_id=1)
 FROM pos_products p JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
 WHERE p.id=1`).Scan(&rev, &cents, &enabled, &name, &count)
		if err != nil || rev != revision || cents != price || enabled != active || name != "Fixture" || count != revision {
			t.Fatal(rev, cents, enabled, name, count, err)
		}
	}
	queue := func(slug, body, token string) (string, posCatalogPreview, string) {
		t.Helper()
		first := call("POST", mcp+slug, body, token, nil, 201)
		retry := call("POST", mcp+slug, body, token, nil, 200)
		if first.Body.String() != retry.Body.String() || strings.Contains(first.Body.String(), "product_id") {
			t.Fatal("unexpected bearer receipt")
		}
		var receipt posCatalogWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID < 1 {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s%s/%d", browser, slug, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		var pending posCatalogWireDetails
		if err := json.Unmarshal(call("GET", path, "", "", cookies, 200).Body.Bytes(), &pending); err != nil || pending.Change == nil || len(pending.Digest) != 64 {
			t.Fatal(pending, err)
		}
		return path, *pending.Change, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, pending.Digest)
	}
	const priceSlug = "pos-price-proposals"
	const avSlug = "pos-availability-proposals"
	const priceBody = `{"request_id":"55555555-0000-4000-8000-000000000001","change":{"product_id":1,"expected_revision":1,"price_minor":300}}`
	const avBody = `{"request_id":"55555555-0000-4000-8000-000000000002","change":{"product_id":1,"expected_revision":1,"active":false}}`
	call("POST", mcp+priceSlug, priceBody, at, nil, 401)
	call("POST", mcp+avSlug, avBody, pt, nil, 401)
	pp, p, pd := queue(priceSlug, priceBody, pt)
	ap, v, ad := queue(avSlug, avBody, at)
	if p.Price != 300 || p.CurrentPrice != 200 || !p.Active || !p.CurrentActive || v.Active || !v.CurrentActive || v.Price != 200 || v.CurrentPrice != 200 {
		t.Fatal(p, v)
	}
	check(1, 200, true)
	call("POST", pp+"/decision", pd, "", otherLogin, 404)
	call("POST", pp+"/decision", pd, "", cookies, 200)
	call("POST", pp+"/decision", pd, "", cookies, 409)
	call("POST", ap+"/decision", ad, "", cookies, 409)
	check(2, 300, true)
	ap, v, ad = queue(avSlug, strings.Replace(strings.Replace(avBody, "000000000002", "000000000003", 1), `"expected_revision":1`, `"expected_revision":2`, 1), at)
	if v.Price != 300 || v.CurrentPrice != 300 || v.Active || !v.CurrentActive {
		t.Fatal(v)
	}
	response := call("POST", ap+"/decision", ad, "", cookies, 200)
	var outcome posCatalogWireDetails
	if err := json.Unmarshal(response.Body.Bytes(), &outcome); err != nil || outcome.ProductRevision == nil || *outcome.ProductRevision != 3 || outcome.Change != nil {
		t.Fatal(outcome, err)
	}
	check(3, 300, false)
}
