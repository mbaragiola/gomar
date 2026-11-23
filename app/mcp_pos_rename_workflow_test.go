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

func TestPostgresMCPPOSRenameSessionBoundaries(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-pos-session"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'operator',$1,TRUE,FALSE,FALSE),(2,'other',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
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
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'administration',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1);
		INSERT INTO pos_products(id,outlet_id,current_revision) VALUES(1,1,1),(2,2,1);
		INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active) VALUES
			(1,1,1,'Original',750,FALSE),(2,2,1,'Other branch',100,TRUE);`)
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	a := &app{db: db, host: "example.com", enableMCP: true, enablePOS: true, enableMCPProposalSessions: true,
		enableMCPWikiProposals: true, enableMCPDocumentProposals: true, enableMCPWikiEditProposals: true,
		mcpClock: func() time.Time { return time.Unix(clock.Load(), 0) }}
	router := a.routes()
	maker := postgresTestCookies(t, router, "operator", password)
	otherLogin := postgresTestCookies(t, router, "operator", password)
	otherUser := postgresTestCookies(t, router, "other", password)
	const origin = "http://example.com"
	const auth = "/api/v1/auth/mcp-proposal-sessions"
	const browser = "/api/v1/entities/1/outlets/1/pos-rename-proposals"
	const management = "/api/v1/entities/1/outlets/1/proposal-sessions"
	const bearer = "/api/v1/mcp/entities/1/outlets/1/pos-rename-proposals"
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
		ID      int64
		Token   string
		Actions []string
	}
	const keyBody = `{"entity_id":1,"outlet_id":1,"actions":["pos_product_rename"]}`
	const legacyBody = `{"entity_id":1,"outlet_id":1}`
	issue := func(path, body string, who []*http.Cookie) key {
		t.Helper()
		var k key
		expect(do(router, "POST", path, body, "", origin, who), 201, &k)
		return k
	}
	payload := func(n, product, revision int, name string) string {
		return fmt.Sprintf(`{"request_id":"88888888-1111-4333-8444-%012d","rename":{"product_id":%d,"expected_revision":%d,"name":%q}}`, n, product, revision, name)
	}
	submit := func(token, body string, status int) posRenameWireReceipt {
		t.Helper()
		w := do(router, "POST", bearer, body, token, "", nil)
		var p posRenameWireReceipt
		expect(w, status, &p)
		for _, private := range []string{"Original", "Renamed", "product_id", "product_revision", "digest"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("agent receipt leaked %s", private)
			}
		}
		return p
	}
	item := func(id int64) string { return fmt.Sprintf("%s/%d", browser, id) }
	detail := func(id int64) posRenameWireDetails {
		t.Helper()
		var p posRenameWireDetails
		expect(do(router, "GET", item(id), "", "", origin, maker), 200, &p)
		return p
	}
	decisionBody := func(digest, verb string, confirm bool) string {
		return fmt.Sprintf(`{"decision":%q,"digest":%q,"confirm":%t}`, verb, digest, confirm)
	}
	decide := func(id int64, digest, verb string, want int) posRenameWireReceipt {
		t.Helper()
		var p posRenameWireReceipt
		expect(do(router, "POST", item(id)+"/decision", decisionBody(digest, verb, true), "", origin, maker), want, &p)
		return p
	}
	check := func(expectedVersion, expectedPrice int, expectedName string) {
		t.Helper()
		var version, price, versions int
		var name string
		var active bool
		err := db.QueryRowContext(ctx, `SELECT p.current_revision,v.price_minor,v.name,v.active,
			(SELECT COUNT(*) FROM pos_product_versions WHERE product_id=1)
			FROM pos_products p JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
			WHERE p.id=1`).Scan(&version, &price, &name, &active, &versions)
		if err != nil || version != expectedVersion || versions != expectedVersion || price != expectedPrice || name != expectedName || active {
			t.Fatal(version, price, name, active, versions, err)
		}
	}

	for _, disabled := range []*app{
		{db: db, host: "example.com", enablePOS: true}, {db: db, host: "example.com", enableMCPProposalSessions: true},
	} {
		expect(do(disabled.routes(), "POST", auth, keyBody, "", origin, maker), 404, nil)
	}
	for _, body := range []string{
		`{"entity_id":1,"outlet_id":1,"actions":[]}`,
		`{"entity_id":1,"outlet_id":1,"actions":["*"]}`,
		`{"entity_id":1,"outlet_id":1,"actions":["pos_product_rename","pos_product_rename"]}`,
		`{"entity_id":1,"outlet_id":1,"actions":["record_payment"]}`,
	} {
		expect(do(router, "POST", auth, body, "", origin, maker), 422, nil)
	}
	expect(do(router, "POST", auth, keyBody, "", "https://foreign.invalid", maker), 403, nil)
	wiki := issue("/api/v1/auth/mcp-proposal-token", legacyBody, maker)
	docs := issue("/api/v1/auth/mcp-document-proposal-token", legacyBody, maker)
	first := issue(auth, keyBody, maker)
	second := issue(auth, keyBody, otherLogin)
	if len(first.Actions) != 1 || first.Actions[0] != posRenameSessionAction {
		t.Fatal(first.Actions)
	}
	expect(do(router, "POST", "/api/v1/auth/mcp-wiki-edit-proposal-token", legacyBody, "", origin, maker), 429, nil)
	expect(do(router, "POST", auth, keyBody, "", origin, maker), 429, nil)
	read := issue("/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, maker)
	const direct = "/api/v1/mcp/entities/1/outlets/1/pos-catalog/"
	checkExact := func(name string, price, revision int64) {
		t.Helper()
		w := do(router, "GET", direct+"1", "", read.Token, "", nil)
		expect(w, 200, nil)
		want := fmt.Sprintf(`{"id":1,"name":%q,"price_minor":%d,"revision":%d,"active":false,"currency":"ARS"}`, name, price, revision)
		if strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("wrong exact read: %s", w.Body.String())
		}
	}
	checkExact("Original", 750, 1)
	unknown := do(router, "GET", direct+"999", "", read.Token, "", nil)
	expect(unknown, 404, nil)
	for _, path := range []string{direct + "2", direct + "0", direct + "-1"} {
		w := do(router, "GET", path, "", read.Token, "", nil)
		expect(w, 404, nil)
		if w.Body.String() != unknown.Body.String() {
			t.Fatal("product existence disclosed")
		}
	}
	expect(do(router, "GET", strings.Replace(direct, "outlets/1", "outlets/2", 1)+"2", "", read.Token, "", nil), 404, nil)
	expect(do(router, "GET", strings.Replace(direct, "entities/1", "entities/2", 1)+"1", "", read.Token, "", nil), 404, nil)
	expect(do(router, "GET", direct+"1?q=name", "", read.Token, "", nil), 422, nil)
	expect(do(router, "GET", direct+"1", "", "", "", nil), 401, nil)
	expect(do((&app{db: db, host: "example.com", enableMCP: true}).routes(), "GET", direct+"1", "", read.Token, "", nil), 404, nil)
	expect(do((&app{db: db, host: "example.com", enablePOS: true}).routes(), "GET", direct+"1", "", read.Token, "", nil), 404, nil)
	for _, wrong := range []key{wiki, docs} {
		submit(wrong.Token, payload(1, 1, 1, "Renamed"), 401)
	}
	expect(do(router, "GET", "/api/v1/mcp/entities", "", first.Token, "", nil), 401, nil)
	expect(do(router, "POST", "/api/v1/mcp/entities/1/outlets/1/wiki-create-proposals",
		`{"request_id":"88888888-1111-4333-8444-000000000001","title":"Fixture","body":"Text"}`, first.Token, "", nil), 401, nil)
	expect(do(router, "POST", strings.Replace(bearer, "outlets/1", "outlets/2", 1), payload(1, 1, 1, "Renamed"), first.Token, "", nil), 404, nil)
	submit(first.Token, payload(1, 2, 1, "Renamed"), 404)
	p := submit(first.Token, payload(1, 1, 1, "Renamed"), 201)
	if replay := submit(first.Token, payload(1, 1, 1, "Renamed"), 200); replay.ID != p.ID {
		t.Fatal(replay)
	}
	submit(first.Token, payload(1, 1, 1, "Changed"), 409)
	submit(second.Token, payload(1, 1, 1, "Renamed"), 409)
	check(1, 750, "Original")
	expect(do(router, "GET", item(p.ID), "", "", origin, otherUser), 404, nil)
	expect(do(router, "GET", item(p.ID), "", "", origin, otherLogin), 404, nil)
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(strings.Repeat("0", 64), "approve", true), "", origin, otherLogin), 404, nil)
	var sessions struct{ Sessions []namedProposalSession }
	w := do(router, "GET", management, "", "", origin, otherLogin)
	expect(w, 200, &sessions)
	if len(sessions.Sessions) != 2 || strings.Contains(w.Body.String(), first.Token) || strings.Contains(w.Body.String(), second.Token) {
		t.Fatal(sessions)
	}
	var list struct{ Proposals []posRenameWireReceipt }
	expect(do(router, "GET", browser, "", "", origin, otherLogin), 200, &list)
	if len(list.Proposals) != 0 {
		t.Fatal(list)
	}
	expect(do(router, "GET", browser, "", "", origin, maker), 200, &list)
	if len(list.Proposals) != 1 || list.Proposals[0].ID != p.ID {
		t.Fatal(list)
	}
	d := detail(p.ID)
	if d.Rename == nil || d.Rename.CurrentName != "Original" || d.Rename.Name != "Renamed" || d.Rename.PriceMinor != 750 || d.Rename.Active || len(d.Digest) != 64 {
		t.Fatal(d)
	}
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", false), "", origin, maker), 422, nil)
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", true), first.Token, origin, nil), 403, nil)
	decide(p.ID, strings.Repeat("0", 64), "approve", 409)
	exec("ALTER TABLE mcp_pos_rename_proposals ADD CONSTRAINT fixture_rename_terminal_fault CHECK(status<>'approved') NOT VALID")
	decide(p.ID, d.Digest, "approve", 500)
	check(1, 750, "Original")
	exec("ALTER TABLE mcp_pos_rename_proposals DROP CONSTRAINT fixture_rename_terminal_fault")
	expect(do(router, "PUT", "/api/v1/entities/1/outlets/1/pos/catalog/1",
		`{"nombre":"Original","precio_centavos":950,"activo":false,"revision":1}`, "", origin, maker), 200, nil)
	decide(p.ID, d.Digest, "approve", 409)
	decide(p.ID, d.Digest, "reject", 200)
	check(2, 950, "Original")
	p = submit(first.Token, payload(2, 1, 2, "Renamed"), 201)
	d = detail(p.ID)
	exec("UPDATE memberships SET role='read_only' WHERE id=1")
	decide(p.ID, d.Digest, "approve", 403)
	w = do(router, "GET", "/api/v1/entities", "", "", origin, otherLogin)
	expect(w, 200, nil)
	if !strings.Contains(w.Body.String(), "mcp_proposal_session_management") || strings.Contains(w.Body.String(), "mcp_pos_rename_proposals") {
		t.Fatal(w.Body.String())
	}
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", auth, second.ID), "", "", origin, otherLogin), 204, nil)
	submit(second.Token, payload(3, 1, 2, "Renamed"), 401)
	exec("UPDATE memberships SET role='administration' WHERE id=1")

	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			codes <- do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", true), "", origin, maker).Code
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
	check(3, 950, "Renamed")
	checkExact("Renamed", 950, 3)
	exec("UPDATE memberships SET role='read_only' WHERE id=1")
	checkExact("Renamed", 950, 3)
	exec("UPDATE memberships SET role='unassigned' WHERE id=1")
	expect(do(router, "GET", direct+"1", "", read.Token, "", nil), 403, nil)
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	receipt := detail(p.ID)
	if receipt.Status != "approved" || receipt.Rename != nil || receipt.Digest != "" || receipt.ProductID == nil || *receipt.ProductID != 1 ||
		receipt.ProductRevision == nil || *receipt.ProductRevision != 3 {
		t.Fatal(receipt)
	}
	if same := submit(first.Token, payload(2, 1, 2, "Renamed"), 200); same.Status != "approved" || same.ProductID != nil {
		t.Fatal(same)
	}

	other := issue(auth, keyBody, otherLogin)
	logoutProposal := submit(other.Token, payload(3, 1, 3, "After logout"), 201)
	logout := do(router, "POST", "/api/v1/auth/logout", "", "", origin, otherLogin)
	if logout.Code != 200 {
		t.Fatalf("logout: %d %s", logout.Code, logout.Body.String())
	}
	submit(other.Token, payload(4, 1, 3, "After logout"), 401)
	expect(do(router, "GET", item(logoutProposal.ID), "", "", origin, maker), 404, nil)
	if err := a.pruneWikiProposals(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var cleared bool
	if err := db.QueryRowContext(ctx, "SELECT status,rename_preview IS NULL FROM mcp_pos_rename_proposals WHERE id=$1", logoutProposal.ID).
		Scan(&state, &cleared); err != nil || state != "revoked" || !cleared {
		t.Fatal(state, cleared, err)
	}
	p = submit(first.Token, payload(5, 1, 3, "After expiry"), 201)
	d = detail(p.ID)
	clock.Add(900)
	submit(first.Token, payload(6, 1, 3, "After expiry"), 401)
	if got := detail(p.ID); got.Status != "expired" || got.Rename != nil {
		t.Fatal(got)
	}
	decide(p.ID, d.Digest, "approve", 410)
	check(3, 950, "Renamed")
}
