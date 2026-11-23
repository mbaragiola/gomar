package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresMCPStockTrackingWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-stock-tracking-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'stock-tracking-workflow',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE),(2,'Another fixture','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE),(3,2,'C',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1);
 INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(88001,1,1),(88002,1,1),(88003,1,1),(88004,2,1);
 INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES(88001,1,1,'Fixture',200,TRUE),(88002,1,1,'Unconfigured',250,TRUE),(88003,1,1,'Inactive',300,FALSE),(88004,2,1,'Other branch',400,TRUE);
 INSERT INTO stock_outlets VALUES(1,TRUE),(2,TRUE);
 INSERT INTO stock_products VALUES(1,88001,TRUE,8),(1,88003,TRUE,4),(2,88004,TRUE,5)`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableStock: true, enableMCPStockTrackingProposals: true, enableMCPStockMinimumProposals: true}
	h := a.routes()
	cookies := postgresTestCookies(t, h, "stock-tracking-workflow", password)
	otherLogin := postgresTestCookies(t, h, "stock-tracking-workflow", password)
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

	const api = "/api/v1/mcp/entities/1/outlets/1/stock-tracking-proposals"
	const browser = "/api/v1/entities/1/outlets/1/stock-tracking-proposals"
	const stock = "/api/v1/entities/1/outlets/1/pos/stock"
	const sales = "/api/v1/entities/1/outlets/1/pos/sales"
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["stock_tracking_change"]}`
	exec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	a.enableMCPStockTrackingProposals = false
	disabled := a.routes()
	w := httptest.NewRecorder()
	disabled.ServeHTTP(w, httptest.NewRequest("POST", "http://example.com"+api, nil))
	if w.Code != 404 {
		t.Fatal("feature-off route registered")
	}
	a.enableMCPStockTrackingProposals = true
	readToken := func(raw string) string {
		t.Helper()
		var got struct {
			Token string `json:"token"`
		}
		if json.Unmarshal([]byte(raw), &got) != nil || len(got.Token) != 43 {
			t.Fatal("synthetic key missing")
		}
		return got.Token
	}
	call("POST", proposalSessionsAuthPath, strings.Replace(issue, `"outlet_id":1`, `"outlet_id":2`, 1), "", cookies, 404)
	token := readToken(call("POST", proposalSessionsAuthPath, issue, "", cookies, 201).Body.String())
	read := readToken(call("POST", "/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, "", cookies, 201).Body.String())
	otherToken := readToken(call("POST", proposalSessionsAuthPath, issue, "", otherLogin, 201).Body.String())
	minimumToken := readToken(call("POST", proposalSessionsAuthPath, strings.Replace(issue, "stock_tracking_change", "stock_minimum_change", 1), "", cookies, 201).Body.String())
	body := func(i, product int, enabled bool) string {
		return fmt.Sprintf(`{"request_id":"55555555-0000-4000-8000-%012d","tracking_change":{"product_id":%d,"expected_revision":1,"tracking_enabled":%t}}`, i, product, enabled)
	}
	call("POST", api, body(1, 88001, false), read, nil, 401)
	call("POST", api, body(1, 88001, false), minimumToken, nil, 401)
	call("POST", api, body(1, 88001, false), token, cookies, 422)
	for _, payload := range []string{
		strings.Replace(body(1, 88001, false), `,"tracking_enabled":false`, "", 1),
		strings.Replace(body(1, 88001, false), `false`, `null`, 1),
		strings.Replace(body(1, 88001, false), `false`, `0`, 1),
		strings.Replace(body(1, 88001, false), `false`, `false,"minimum":8`, 1),
	} {
		call("POST", api, payload, token, nil, 422)
	}
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/2/", 1), body(1, 88001, false), token, nil, 404)
	call("POST", strings.Replace(api, "/entities/1/", "/entities/2/", 1), body(1, 88001, false), token, nil, 404)
	for _, id := range []int{88002, 88003, 88004, 999} {
		call("POST", api, body(1, id, false), token, nil, 404)
	}
	call("POST", api, body(1, 88001, true), token, nil, 422)
	ledger := func() string {
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
			t.Fatal("cannot snapshot stock and sales")
		}
		return digest
	}
	check := func(tracking bool, minimum, approved, count, salesCount, moves, balance int) {
		t.Helper()
		var gotMinimum, gotApproved, gotCount, gotSales, gotMoves, gotBalance, rev int
		var gotTracking, outletEnabled bool
		err := db.QueryRowContext(ctx, `SELECT t.minimum,t.enabled,o.enabled,p.current_revision,
 (SELECT count(*) FROM mcp_stock_tracking_proposals WHERE status='approved'),
 (SELECT count(*) FROM stock_products),(SELECT count(*) FROM pos_sales),
 (SELECT count(*) FROM stock_movements),COALESCE((SELECT sum(delta) FROM stock_movements WHERE outlet_id=1 AND product_id=88001),0)
 FROM stock_products t JOIN stock_outlets o ON o.outlet_id=t.outlet_id
 JOIN pos_products p ON p.id=t.product_id AND p.outlet_id=t.outlet_id
 WHERE t.outlet_id=1 AND t.product_id=88001`).Scan(&gotMinimum, &gotTracking, &outletEnabled, &rev, &gotApproved, &gotCount, &gotSales, &gotMoves, &gotBalance)
		if err != nil || gotMinimum != minimum || gotTracking != tracking || !outletEnabled || rev != 1 || gotApproved != approved ||
			gotCount != count || gotSales != salesCount || gotMoves != moves || gotBalance != balance {
			t.Fatal(gotMinimum, gotTracking, outletEnabled, rev, gotApproved, gotCount, gotSales, gotMoves, gotBalance, err)
		}
	}
	type queued struct{ path, decision, digest string }
	queue := func(i int, enabled bool, minimum int64) queued {
		t.Helper()
		payload := body(i, 88001, enabled)
		first := call("POST", api, payload, token, nil, 201)
		retry := call("POST", api, payload, token, nil, 200)
		if first.Body.String() != retry.Body.String() || strings.Contains(first.Body.String(), "tracking_enabled") || strings.Contains(first.Body.String(), "product_id") {
			t.Fatal("proposal receipt leaked review fields")
		}
		var receipt stockTrackingWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID < 1 {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		var preview stockTrackingWireDetails
		err := json.Unmarshal(call("GET", path, "", "", cookies, 200).Body.Bytes(), &preview)
		p := preview.TrackingChange
		if err != nil || p == nil || p.Product != 88001 || p.Name != "Fixture" || p.Revision != 1 ||
			p.TrackingEnabled != enabled || p.Previous == enabled || p.Minimum != minimum || len(preview.Digest) != 64 {
			t.Fatal(preview, p, err)
		}
		ack := ""
		if !enabled {
			ack = `,"acknowledge_no_backfill":true`
		}
		return queued{path, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true%s}`, preview.Digest, ack), preview.Digest}
	}
	sale := func(i int, qty int) string {
		return fmt.Sprintf(`{"id_solicitud":"11111111-0000-4000-8000-%012d","actor_id":1,"items":[{"producto_id":88001,"revision":1,"cantidad":%d}]}`, i, qty)
	}
	intake := `{"id_solicitud":"22222222-0000-4000-8000-000000000001","tipo":"recepcion","cantidad":10}`
	path := stock + "/products/88001/movements"
	intakeReceipt := call("POST", path, intake, "", cookies, 201).Body.String()
	firstSale := call("POST", sales, sale(1, 2), "", cookies, 201).Body.String()
	check(true, 8, 0, 3, 1, 2, 8)

	first := queue(10, false, 8)
	competing := queue(11, false, 8)
	call("POST", api, body(10, 88001, false), otherToken, nil, 409)
	check(true, 8, 0, 3, 1, 2, 8)
	call("POST", first.path+"/decision", first.decision, "", otherLogin, 404)
	call("POST", first.path+"/decision", strings.Replace(first.decision, `,"acknowledge_no_backfill":true`, "", 1), "", cookies, 422)
	call("POST", first.path+"/decision", strings.Replace(first.decision, `"acknowledge_no_backfill":true`, `"acknowledge_no_backfill":false`, 1), "", cookies, 422)
	before := ledger()
	approval := call("POST", first.path+"/decision", first.decision, "", cookies, 200)
	var receipt stockTrackingWireDetails
	if err := json.Unmarshal(approval.Body.Bytes(), &receipt); err != nil || receipt.TrackingEnabled == nil || *receipt.TrackingEnabled ||
		receipt.ProductID == nil || *receipt.ProductID != 88001 || receipt.TrackingChange != nil {
		t.Fatal(approval.Body.String(), err)
	}
	call("POST", first.path+"/decision", first.decision, "", cookies, 409)
	call("POST", competing.path+"/decision", competing.decision, "", cookies, 409)
	if got := ledger(); got != before {
		t.Fatal("disabling touched native ledger or other settings")
	}
	check(false, 8, 1, 3, 1, 2, 8)
	if retry := call("POST", api, body(10, 88001, false), token, nil, 200).Body.String(); strings.Contains(retry, "tracking_enabled") || strings.Contains(retry, "product_id") {
		t.Fatal("terminal bearer receipt was not redacted")
	}

	// Legacy offline wire is accepted while paused without a deduction.
	if call("POST", sales, sale(1, 2), "", cookies, 200).Body.String() != firstSale ||
		call("POST", path, intake, "", cookies, 200).Body.String() != intakeReceipt {
		t.Fatal("accepted native receipts changed after toggle")
	}
	secondSale := call("POST", sales, sale(2, 3), "", cookies, 201).Body.String()
	call("POST", path, strings.Replace(intake, "000000000001", "000000000002", 1), "", cookies, 409)
	check(false, 8, 1, 3, 2, 2, 8)

	globalStale := queue(12, true, 8)
	call("PUT", stock+"/settings", `{"activo":false}`, "", cookies, 200)
	call("POST", globalStale.path+"/decision", globalStale.decision, "", cookies, 409)
	call("POST", api, body(13, 88001, true), token, nil, 404)
	call("PUT", stock+"/settings", `{"activo":true}`, "", cookies, 200)
	check(false, 8, 1, 3, 2, 2, 8)

	minimumStale := queue(14, true, 8)
	call("PUT", stock+"/products/88001", `{"activo":false,"minimo":9}`, "", cookies, 200)
	call("POST", minimumStale.path+"/decision", minimumStale.decision, "", cookies, 409)
	check(false, 9, 1, 3, 2, 2, 8)

	revokedGrant := queue(15, true, 9)
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1")
	call("POST", revokedGrant.path+"/decision", revokedGrant.decision, "", cookies, 404)
	call("POST", api, body(15, 88001, true), token, nil, 404)
	exec("INSERT INTO outlet_grants VALUES(1,1); UPDATE memberships SET role='accounting' WHERE id=1")
	call("POST", revokedGrant.path+"/decision", revokedGrant.decision, "", cookies, 403)
	call("POST", proposalSessionsAuthPath, issue, "", cookies, 403)
	exec("UPDATE memberships SET role='administration' WHERE id=1")

	rejected := queue(16, true, 9)
	rejection := call("POST", rejected.path+"/decision", fmt.Sprintf(`{"decision":"reject","digest":%q,"confirm":true}`, rejected.digest), "", cookies, 200).Body.String()
	if strings.Contains(rejection, "tracking_enabled") || strings.Contains(rejection, "product_id") {
		t.Fatal("rejection leaked result fields")
	}
	check(false, 9, 1, 3, 2, 2, 8)
	on := queue(17, true, 9)
	before = ledger()
	call("POST", on.path+"/decision", on.decision, "", cookies, 200)
	if ledger() != before {
		t.Fatal("reactivating changed old sales, stock movements or other settings")
	}
	check(true, 9, 2, 3, 2, 2, 8)
	if call("POST", sales, sale(2, 3), "", cookies, 200).Body.String() != secondSale {
		t.Fatal("paused sale now reports different receipt")
	}
	check(true, 9, 2, 3, 2, 2, 8)
	call("POST", sales, sale(3, 1), "", cookies, 201)
	check(true, 9, 2, 3, 3, 3, 7)
	// Two valid pending requests for the same old state serialize under the
	// same outlet lock. Exactly one applies; the other cannot write twice.
	raceA, raceB := queue(18, false, 9), queue(19, false, 9)
	before = ledger()
	codes := make(chan int, 2)
	for _, proposal := range []queued{raceA, raceB} {
		go func(proposal queued) {
			req := httptest.NewRequestWithContext(ctx, "POST", "http://example.com"+proposal.path+"/decision", strings.NewReader(proposal.decision))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://example.com")
			for _, cookie := range cookies {
				req.AddCookie(cookie)
				if cookie.Name == "csrftoken" {
					req.Header.Set("X-CSRFToken", cookie.Value)
				}
			}
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			codes <- out.Code
		}(proposal)
	}
	x, y := <-codes, <-codes
	if !(x == 200 && y == 409 || x == 409 && y == 200) || ledger() != before {
		t.Fatalf("concurrent approval effects: %d, %d", x, y)
	}
	check(false, 9, 3, 3, 3, 3, 7)

}
