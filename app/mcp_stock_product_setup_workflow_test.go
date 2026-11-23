package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPStockProductSetupWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-stock-setup-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'stock-setup-workflow',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE),(2,'Another fixture','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE),(3,2,'C',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1);
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(88001,1,1),(88002,1,1),(88003,1,1),(88004,2,1),(88005,1,1),(88006,1,1),(88007,1,1),(88008,1,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(88001,1,1,'Fixture',200,TRUE),(88002,1,1,'Unconfigured',250,TRUE),(88003,1,1,'Inactive',300,FALSE),(88004,2,1,'Other branch',400,TRUE),(88005,1,1,'Competing native configuration',500,TRUE),(88006,1,1,'Branch pause',600,TRUE),(88007,1,1,'Rejection',700,TRUE),(88008,1,1,'Catalog revision',800,TRUE);
 INSERT INTO stock_outlets VALUES(1,TRUE),(2,TRUE);
 INSERT INTO stock_products VALUES(1,88002,FALSE,8),(1,88003,TRUE,4),(2,88004,TRUE,5)`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableStock: true, enableMCPStockTrackingProposals: true, enableMCPStockProductSetupProposals: true, enableMCPStockMinimumProposals: true}
	h := a.routes()
	cookies := postgresTestCookies(t, h, "stock-setup-workflow", password)
	otherLogin := postgresTestCookies(t, h, "stock-setup-workflow", password)
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

	const api = "/api/v1/mcp/entities/1/outlets/1/stock-product-setup-proposals"
	const browser = "/api/v1/entities/1/outlets/1/stock-product-setup-proposals"
	const stock = "/api/v1/entities/1/outlets/1/pos/stock"
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["stock_product_setup"]}`
	off := app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableStock: true}
	noRoute := httptest.NewRecorder()
	off.routes().ServeHTTP(noRoute, httptest.NewRequest("POST", "http://example.com"+api, nil))
	if noRoute.Code != 404 {
		t.Fatal("setup route enabled by default")
	}
	key := func(input string, login []*http.Cookie) string {
		t.Helper()
		var data struct{ Token string }
		if err := json.Unmarshal(call("POST", proposalSessionsAuthPath, input, "", login, 201).Body.Bytes(), &data); err != nil || len(data.Token) != 43 {
			t.Fatal("cannot issue synthetic key")
		}
		return data.Token
	}
	token, differentLogin := key(issue, cookies), key(issue, otherLogin)
	trackingToken := key(strings.Replace(issue, "stock_product_setup", "stock_tracking_change", 1), cookies)
	body := func(id, product, minimum int) string {
		return fmt.Sprintf(`{"request_id":"55555555-0000-4000-8000-%012d","stock_setup":{"product_id":%d,"expected_revision":1,"minimum":%d}}`, id, product, minimum)
	}
	call("POST", proposalSessionsAuthPath, strings.Replace(issue, `"outlet_id":1`, `"outlet_id":2`, 1), "", cookies, 404)
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/2/", 1), body(1, 88001, 0), token, nil, 404)
	call("POST", api, body(1, 88001, 0), trackingToken, nil, 401)
	call("POST", api, body(1, 88001, 0), token, cookies, 422)
	call("POST", api, strings.Replace(body(1, 88001, 0), `,"minimum":0`, "", 1), token, nil, 422)
	call("POST", api, strings.Replace(body(1, 88001, 0), `"minimum":0`, `"minimum":null`, 1), token, nil, 422)
	call("POST", api, strings.Replace(body(1, 88001, 0), `"minimum":0`, `"minimum":0,"tracking_enabled":true`, 1), token, nil, 422)
	call("POST", api, body(1, 88001, -1), token, nil, 422)
	call("POST", api, body(1, 88001, 1000001), token, nil, 422)
	call("POST", api, body(1, 88002, 4), token, nil, 409) // Existing disabled configuration, never overwrite.
	call("POST", api, body(1, 88003, 4), token, nil, 404) // Inactive catalog item.
	call("POST", api, body(1, 88004, 4), token, nil, 404) // Foreign product.
	call("POST", api, body(1, 999, 4), token, nil, 404)
	type queued struct{ path, decision, digest string }
	queue := func(id, product, minimum int, name string) queued {
		t.Helper()
		payload := body(id, product, minimum)
		first := call("POST", api, payload, token, nil, 201)
		if retry := call("POST", api, payload, token, nil, 200); retry.Body.String() != first.Body.String() ||
			strings.Contains(retry.Body.String(), "minimum") || strings.Contains(retry.Body.String(), "tracking_enabled") || strings.Contains(retry.Body.String(), "product_id") {
			t.Fatal("unbounded or changed pending receipt")
		}
		var receipt stockProductSetupWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID <= 0 || receipt.Status != "pending" {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		var detail stockProductSetupWireDetails
		if err := json.Unmarshal(call("GET", path, "", "", cookies, 200).Body.Bytes(), &detail); err != nil ||
			detail.StockSetup == nil || detail.StockSetup.Product != int64(product) || detail.StockSetup.Minimum != int64(minimum) ||
			detail.StockSetup.Name != name || detail.StockSetup.Revision != 1 || detail.StockSetup.TrackingEnabled || len(detail.Digest) != 64 {
			t.Fatal(detail, err)
		}
		return queued{path, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, detail.Digest), detail.Digest}
	}
	check := func(product int, present, enabled bool, minimum int) {
		t.Helper()
		var got bool
		var min int
		err := db.QueryRowContext(ctx, "SELECT enabled,minimum FROM stock_products WHERE outlet_id=1 AND product_id=$1", product).Scan(&got, &min)
		if present && (err != nil || got != enabled || min != minimum) || !present && err != sql.ErrNoRows {
			t.Fatal("native stock setup mismatch", product, got, min, err)
		}
	}
	fingerprint := func() string {
		t.Helper()
		var digest string
		err := db.QueryRowContext(ctx, `SELECT md5(concat(
  (SELECT json_agg(x ORDER BY id)::text FROM stock_movements x),
  (SELECT json_agg(x ORDER BY id)::text FROM pos_sales x),
  (SELECT json_agg(x ORDER BY sale_id,product_id)::text FROM pos_sale_items x),
  (SELECT json_agg(x ORDER BY product_id,revision)::text FROM pos_product_versions x),
  (SELECT json_agg(x ORDER BY outlet_id)::text FROM stock_outlets x),
  (SELECT json_agg(x ORDER BY outlet_id,product_id)::text FROM stock_products x WHERE product_id<>88001)
 ))`).Scan(&digest)
		if err != nil || digest == "" {
			t.Fatal("cannot snapshot native stock")
		}
		return digest
	}
	sales := "/api/v1/entities/1/outlets/1/pos/sales"
	sale := func(id int) string {
		return fmt.Sprintf(`{"id_solicitud":"22222222-0000-4000-8000-%012d","actor_id":1,"items":[{"producto_id":88001,"revision":1,"cantidad":2}]}`, id)
	}
	oldSale := call("POST", sales, sale(1), "", cookies, 201).Body.String()
	original := fingerprint()
	first := queue(10, 88001, 0, "Fixture")
	other := queue(11, 88001, 10, "Fixture")
	call("POST", api, body(10, 88001, 0), differentLogin, nil, 409)
	call("POST", first.path+"/decision", first.decision, "", otherLogin, 404)
	check(88001, false, false, 0)
	result := call("POST", first.path+"/decision", first.decision, "", cookies, 200)
	var receipt stockProductSetupWireDetails
	if err := json.Unmarshal(result.Body.Bytes(), &receipt); err != nil || receipt.StockSetup != nil || receipt.ProductID == nil ||
		*receipt.ProductID != 88001 || receipt.Minimum == nil || *receipt.Minimum != 0 || receipt.TrackingEnabled == nil || *receipt.TrackingEnabled {
		t.Fatal(result.Body.String(), err)
	}
	if got := call("GET", first.path, "", "", cookies, 200).Body.String(); got != result.Body.String() {
		t.Fatal("browser receipt did not retain the exact disabled setup")
	}
	if !strings.Contains(call("GET", browser, "", "", cookies, 200).Body.String(), `"tracking_enabled":false`) {
		t.Fatal("disabled decision missing from browser list")
	}
	replayed := call("POST", api, body(10, 88001, 0), token, nil, 200)
	if strings.Contains(replayed.Body.String(), "minimum") || strings.Contains(replayed.Body.String(), "tracking_enabled") ||
		strings.Contains(replayed.Body.String(), "product_id") || !strings.Contains(replayed.Body.String(), "approved") {
		t.Fatal("terminal bearer receipt exposed result fields")
	}
	call("POST", first.path+"/decision", first.decision, "", cookies, 409)
	call("POST", other.path+"/decision", other.decision, "", cookies, 409)
	if fingerprint() != original {
		t.Fatal("setup changed ledger, catalog, branch settings or another product")
	}
	check(88001, true, false, 0)
	call("POST", sales, sale(2), "", cookies, 201)
	if retry := call("POST", sales, sale(1), "", cookies, 200).Body.String(); retry != oldSale {
		t.Fatal("old sale receipt changed")
	}
	var movements int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM stock_movements").Scan(&movements); err != nil || movements != 0 {
		t.Fatal("setup initiated tracking", movements, err)
	}

	// Only the established, separately keyed tracking action enables
	// deductions. The two pre-activation sales remain out of the ledger.
	const trackingAPI = "/api/v1/mcp/entities/1/outlets/1/stock-tracking-proposals"
	const trackingBrowser = "/api/v1/entities/1/outlets/1/stock-tracking-proposals"
	var activate stockTrackingWireReceipt
	raw := `{"request_id":"55555555-0000-4000-8000-000000000130","tracking_change":{"product_id":88001,"expected_revision":1,"tracking_enabled":true}}`
	if err := json.Unmarshal(call("POST", trackingAPI, raw, trackingToken, nil, 201).Body.Bytes(), &activate); err != nil || activate.ID <= 0 {
		t.Fatal("cannot queue separate activation", err)
	}
	actPath := fmt.Sprintf("%s/%d", trackingBrowser, activate.ID)
	var act stockTrackingWireDetails
	if err := json.Unmarshal(call("GET", actPath, "", "", cookies, 200).Body.Bytes(), &act); err != nil || len(act.Digest) != 64 {
		t.Fatal("cannot review separate activation", err)
	}
	call("POST", actPath+"/decision", fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, act.Digest), "", cookies, 200)
	check(88001, true, true, 0)
	if retry := call("POST", sales, sale(2), "", cookies, 200); retry.Code != 200 {
		t.Fatal("paused sale replay changed")
	}
	call("POST", sales, sale(3), "", cookies, 201)
	var totalDelta int
	if err := db.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(delta),0) FROM stock_movements").Scan(&movements, &totalDelta); err != nil || movements != 1 || totalDelta != -2 {
		t.Fatal("unexpected deductions before or after separate activation", movements, totalDelta, err)
	}
	if !strings.Contains(call("GET", first.path, "", "", cookies, 200).Body.String(), `"tracking_enabled":false`) {
		t.Fatal("original creation receipt now misrepresents the historical disabled state")
	}

	competing := queue(12, 88005, 4, "Competing native configuration")
	call("PUT", stock+"/products/88005", `{"activo":true,"minimo":9}`, "", cookies, 200)
	call("POST", competing.path+"/decision", competing.decision, "", cookies, 409)
	check(88005, true, true, 9)
	paused := queue(13, 88006, 4, "Branch pause")
	call("PUT", stock+"/settings", `{"activo":false}`, "", cookies, 200)
	call("POST", paused.path+"/decision", paused.decision, "", cookies, 409)
	call("PUT", stock+"/settings", `{"activo":true}`, "", cookies, 200)
	check(88006, false, false, 0)
	rejected := queue(14, 88007, 4, "Rejection")
	out := call("POST", rejected.path+"/decision", fmt.Sprintf(`{"decision":"reject","digest":%q,"confirm":true}`, rejected.digest), "", cookies, 200).Body.String()
	if strings.Contains(out, "minimum") || strings.Contains(out, "tracking_enabled") || strings.Contains(out, "product_id") {
		t.Fatal("rejection exposed result fields")
	}
	check(88007, false, false, 0)

	stale := queue(15, 88008, 4, "Catalog revision")
	call("PUT", "/api/v1/entities/1/outlets/1/pos/catalog/88008", `{"nombre":"Changed","precio_centavos":800,"activo":true,"revision":1}`, "", cookies, 200)
	call("POST", stale.path+"/decision", stale.decision, "", cookies, 409)
	check(88008, false, false, 0)
	if _, err := db.ExecContext(ctx, "DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1"); err != nil {
		t.Fatal(err)
	}
	call("POST", api, body(1, 88006, 2), token, nil, 404)
	call("POST", paused.path+"/decision", paused.decision, "", cookies, 404)
	if _, err := db.ExecContext(ctx, "INSERT INTO outlet_grants VALUES(1,1); UPDATE memberships SET role='purchasing' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	call("POST", proposalSessionsAuthPath, issue, "", cookies, 403)
	call("POST", paused.path+"/decision", paused.decision, "", cookies, 403)
}
