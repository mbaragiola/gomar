package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresMCPStockReceiptProposalBoundaries(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-stock-workflow"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'stock-editor',$1,TRUE,FALSE,FALSE),(2,'stock-other',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','30111111118',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Second',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES
			(1,1,1,'administration',TRUE),(2,2,1,'accounting',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1);
		INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1),(2,2,1);
		INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES
			(1,1,1,'Synthetic product',100,TRUE),(2,2,1,'Foreign branch',100,TRUE);
		INSERT INTO stock_outlets VALUES(1,TRUE),(2,TRUE);
		INSERT INTO stock_products VALUES(1,1,TRUE,8),(2,2,TRUE,8);`)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC).Unix())
	a := &app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableStock: true, enableMCPDocumentProposals: true,
		enableMCPWikiProposals: true, enableMCPWikiEditProposals: true, enableMCPStockReceiptProposals: true, mcpClock: func() time.Time { return time.Unix(clock.Load(), 0) }}
	router := a.routes()
	actor := postgresTestCookies(t, router, "stock-editor", password)
	other := postgresTestCookies(t, router, "stock-other", password)
	const origin = "http://example.com"
	const auth = "/api/v1/auth/mcp-stock-receipt-proposal-token"
	const wikiAuth = "/api/v1/auth/mcp-proposal-token"
	const docsAuth = "/api/v1/auth/mcp-document-proposal-token"
	const editAuth = "/api/v1/auth/mcp-wiki-edit-proposal-token"
	const browser = "/api/v1/entities/1/outlets/1/stock-receipt-proposals"
	const bearer = "/api/v1/mcp/entities/1/outlets/1/stock-receipt-proposals"
	do := func(h http.Handler, method, path, body, token, source string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body)).WithContext(ctx)
		if source != "" {
			r.Header.Set("Origin", source)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int, dst any) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("got %d, want %d: %s", w.Code, status, w.Body.String())
		}
		if status != 404 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
	}
	type key struct {
		ID    int64
		Token string
	}
	const keyBody = `{"entity_id":1,"outlet_id":1}`
	issue := func(path string, cookies []*http.Cookie) key {
		t.Helper()
		var k key
		expect(do(router, "POST", path, keyBody, "", origin, cookies), 201, &k)
		return k
	}
	body := func(n, id, qty int) string {
		return fmt.Sprintf(`{"request_id":"88888888-1111-4333-8444-%012d","receipt":{"product_id":%d,"quantity":%d}}`, n, id, qty)
	}
	submit := func(token, raw string, status int) stockReceiptWireReceipt {
		t.Helper()
		w := do(router, "POST", bearer, raw, token, "", nil)
		var p stockReceiptWireReceipt
		expect(w, status, &p)
		for _, private := range []string{"Synthetic product", "movement_id", "product_id", "quantity", "digest"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("agent receipt leaked %s", private)
			}
		}
		return p
	}
	item := func(id int64) string { return fmt.Sprintf("%s/%d", browser, id) }
	detail := func(id int64) stockReceiptWireDetails {
		t.Helper()
		var p stockReceiptWireDetails
		expect(do(router, "GET", item(id), "", "", origin, actor), 200, &p)
		return p
	}
	decisionBody := func(digest, verb string, confirm bool) string {
		return fmt.Sprintf(`{"decision":%q,"digest":%q,"confirm":%t}`, verb, digest, confirm)
	}
	decide := func(id int64, digest, verb string, want int) stockReceiptWireReceipt {
		t.Helper()
		var p stockReceiptWireReceipt
		expect(do(router, "POST", item(id)+"/decision", decisionBody(digest, verb, true), "", origin, actor), want, &p)
		return p
	}
	count := func(moves, total int) {
		t.Helper()
		var n, qty, sales, ap int
		err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM stock_movements), (SELECT coalesce(sum(delta),0) FROM stock_movements),
			(SELECT COUNT(*) FROM pos_sales),(SELECT COUNT(*) FROM ap_payments)`).Scan(&n, &qty, &sales, &ap)
		if err != nil || n != moves || qty != total || sales != 0 || ap != 0 {
			t.Fatal(n, qty, sales, ap, err)
		}
	}

	for _, disabled := range []*app{
		{db: db, host: "example.com", enablePOS: true, enableStock: true},
		{db: db, host: "example.com", enableMCPStockReceiptProposals: true, enablePOS: true},
		{db: db, host: "example.com", enableMCPStockReceiptProposals: true, enableStock: true},
	} {
		expect(do(disabled.routes(), "POST", auth, keyBody, "", origin, actor), 404, nil)
	}
	expect(do(router, "POST", auth, keyBody, "", "https://foreign.invalid", actor), 403, nil)
	expect(do(router, "POST", auth, keyBody, "", origin, nil), 403, nil)
	buyer := issue(auth, other) // Accounting is an existing stock operation role.
	otherProposal := submit(buyer.Token, body(1, 1, 4), 201)
	expect(do(router, "GET", item(otherProposal.ID), "", "", origin, actor), 404, nil)
	issue(auth, other)
	issue(auth, other)
	expect(do(router, "POST", auth, keyBody, "", origin, other), 429, nil) // fourth for a single purpose
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", auth, buyer.ID), "", "", origin, other), 204, nil)
	issue(auth, other) // a revoked key no longer counts as active

	docs := issue(docsAuth, actor)
	edit := issue(editAuth, actor)
	key1 := issue(auth, actor)
	wiki := issue(wikiAuth, actor)                                         // one active key for each of the four purposes
	expect(do(router, "POST", auth, keyBody, "", origin, actor), 429, nil) // fifth active key
	read := issue("/api/v1/auth/mcp-token", actor)
	for _, wrong := range []key{docs, edit, wiki, read} {
		submit(wrong.Token, body(1, 1, 7), 401)
	}
	expect(do(router, "GET", "/api/v1/mcp/entities", "", key1.Token, "", nil), 401, nil)
	expect(do(router, "POST", "/api/v1/mcp/entities/1/outlets/1/document-draft-proposals",
		fmt.Sprintf(`{"request_id":"88888888-1111-4333-8444-000000000001","draft":%s}`, proposalInvoiceFixture), key1.Token, "", nil), 401, nil)
	expect(do(router, "POST", "/api/v1/mcp/entities/1/outlets/1/wiki-edit-proposals",
		`{"request_id":"88888888-1111-4333-8444-000000000001","edit":{"article_id":1,"expected_version":1,"title":"Fixture","body":"Text"}}`,
		key1.Token, "", nil), 401, nil)
	expect(do(router, "POST", strings.Replace(bearer, "outlets/1", "outlets/2", 1), body(1, 1, 7), key1.Token, "", nil), 404, nil)
	submit(key1.Token, body(1, 2, 7), 409)
	p := submit(key1.Token, body(1, 1, 7), 201)
	if same := submit(key1.Token, body(1, 1, 7), 200); same.ID != p.ID {
		t.Fatal(same)
	}
	submit(key1.Token, body(1, 1, 8), 409)
	count(0, 0)
	d := detail(p.ID)
	if d.Receipt == nil || d.Receipt.ProductID != 1 || d.Receipt.Quantity != 7 || d.Receipt.ProductRevision != 1 ||
		d.Receipt.ProductName != "Synthetic product" || d.Receipt.StockMinimum != 8 || len(d.Digest) != 64 {
		t.Fatal(d)
	}
	expect(do(router, "GET", item(p.ID), "", "", origin, other), 404, nil)
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", false), "", origin, actor), 422, nil)
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", true), key1.Token, origin, nil), 403, nil)
	decide(p.ID, strings.Repeat("0", 64), "approve", 409)
	exec("UPDATE stock_products SET minimum=15 WHERE outlet_id=1 AND product_id=1")
	decide(p.ID, d.Digest, "approve", 409)
	if same := submit(key1.Token, body(1, 1, 7), 200); same.ID != p.ID {
		t.Fatal(same)
	}
	decide(p.ID, d.Digest, "reject", 200)
	p = submit(key1.Token, body(2, 1, 7), 201)
	d = detail(p.ID)
	exec("UPDATE memberships SET role='read_only' WHERE id=1")
	decide(p.ID, d.Digest, "approve", 403)
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	exec("ALTER TABLE mcp_stock_receipt_proposals ADD CONSTRAINT fixture_stock_terminal_fault CHECK(status<>'approved') NOT VALID")
	decide(p.ID, d.Digest, "approve", 500)
	count(0, 0)
	if got := detail(p.ID); got.Status != "pending" || got.Receipt == nil {
		t.Fatal(got)
	}
	exec("ALTER TABLE mcp_stock_receipt_proposals DROP CONSTRAINT fixture_stock_terminal_fault")
	expect(do(router, "POST", "/api/v1/entities/1/outlets/1/pos/stock/products/1/movements",
		`{"id_solicitud":"88888888-1111-4333-8444-000000000099","tipo":"recepcion","cantidad":3}`, "", origin, actor), 201, nil)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			codes <- do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", true), "", origin, actor).Code
		})
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	count(2, 10)
	approved := detail(p.ID)
	if approved.MovementID == nil || approved.Receipt != nil || approved.Digest != "" {
		t.Fatal(approved)
	}
	var kind string
	var qty, delta, actorID int
	if err := db.QueryRowContext(ctx, "SELECT kind,quantity,delta,actor_id FROM stock_movements WHERE id=$1", *approved.MovementID).
		Scan(&kind, &qty, &delta, &actorID); err != nil || kind != "recepcion" || qty != 7 || delta != 7 || actorID != 1 {
		t.Fatal(kind, qty, delta, actorID, err)
	}
	if same := submit(key1.Token, body(2, 1, 7), 200); same.Status != "approved" || same.MovementID != nil {
		t.Fatal(same)
	}

	p = submit(key1.Token, body(3, 1, 7), 201)
	d = detail(p.ID)
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", docsAuth, key1.ID), "", "", origin, actor), 204, nil)
	if got := detail(p.ID); got.Status != "pending" {
		t.Fatal(got)
	}
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", auth, key1.ID), "", "", origin, actor), 204, nil)
	decide(p.ID, d.Digest, "approve", 410)
	next := issue(auth, actor)
	p = submit(next.Token, body(4, 1, 7), 201)
	clock.Add(900)
	issue(auth, actor) // expired keys no longer count as active
	if got := detail(p.ID); got.Status != "expired" || got.Receipt != nil {
		t.Fatal(got)
	}
	if err := a.pruneWikiProposals(ctx); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM mcp_stock_receipt_proposals WHERE receipt_preview IS NOT NULL").Scan(&stored); err != nil || stored != 0 {
		t.Fatal(stored, err)
	}
	count(2, 10)
}
