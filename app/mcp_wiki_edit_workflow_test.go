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

func TestPostgresMCPWikiEditProposalBoundaries(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-wiki-edit-proposal"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'editor',$1,TRUE,FALSE,FALSE),(2,'other',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
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
		INSERT INTO knowledge_articles(id,outlet_id,title,body,search_text,version,created_at,updated_at)
			VALUES(1,1,'Original','Text before','original',1,'time','time'),(2,2,'Other','Other text','other',1,'time','time');
		INSERT INTO knowledge_article_versions(article_id,version,actor_id,recorded_at) VALUES(1,1,1,'time'),(2,1,1,'time');`)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC).Unix())
	a := &app{db: db, host: "example.com", enableMCP: true, enableMCPWikiProposals: true, enableMCPDocumentProposals: true, enableMCPWikiEditProposals: true,
		mcpClock: func() time.Time { return time.Unix(clock.Load(), 0) }}
	router := a.routes()
	disabled := (&app{db: db, host: "example.com", enableMCPWikiProposals: true}).routes()
	actor := postgresTestCookies(t, router, "editor", password)
	other := postgresTestCookies(t, router, "other", password)
	const origin = "http://example.com"
	const auth = "/api/v1/auth/mcp-wiki-edit-proposal-token"
	const wikiAuth = "/api/v1/auth/mcp-proposal-token"
	const docsAuth = "/api/v1/auth/mcp-document-proposal-token"
	const browser = "/api/v1/entities/1/outlets/1/wiki-edit-proposals"
	const bearer = "/api/v1/mcp/entities/1/outlets/1/wiki-edit-proposals"
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
	issue := func(path string) key {
		t.Helper()
		var k key
		expect(do(router, "POST", path, keyBody, "", origin, actor), 201, &k)
		return k
	}
	body := func(n, article, version int, text string) string {
		return fmt.Sprintf(`{"request_id":"88888888-1111-4333-8444-%012d","edit":{"article_id":%d,"expected_version":%d,"title":"Reviewed","body":%q}}`, n, article, version, text)
	}
	submit := func(token, raw string, status int) wikiEditWireReceipt {
		t.Helper()
		w := do(router, "POST", bearer, raw, token, "", nil)
		var receipt wikiEditWireReceipt
		expect(w, status, &receipt)
		for _, private := range []string{"Text before", "Text after", "article_id", "digest", "body"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("agent receipt leaked %s", private)
			}
		}
		return receipt
	}
	item := func(id int64) string { return fmt.Sprintf("%s/%d", browser, id) }
	detail := func(id int64) wikiEditWireDetails {
		t.Helper()
		var d wikiEditWireDetails
		expect(do(router, "GET", item(id), "", "", origin, actor), 200, &d)
		return d
	}
	decisionBody := func(digest, verb string, confirm bool) string {
		return fmt.Sprintf(`{"decision":%q,"digest":%q,"confirm":%t}`, verb, digest, confirm)
	}
	decide := func(id int64, digest, verb string, want int) wikiEditWireReceipt {
		t.Helper()
		var receipt wikiEditWireReceipt
		expect(do(router, "POST", item(id)+"/decision", decisionBody(digest, verb, true), "", origin, actor), want, &receipt)
		return receipt
	}
	check := func(version int64, text string) {
		t.Helper()
		var rev, versions, count int64
		var got string
		err := db.QueryRowContext(ctx, `SELECT version,body,(SELECT COUNT(*) FROM knowledge_article_versions WHERE article_id=1),
			(SELECT COUNT(*) FROM knowledge_articles) FROM knowledge_articles WHERE id=1`).Scan(&rev, &got, &versions, &count)
		if err != nil || rev != version || versions != version || got != text || count != 2 {
			t.Fatal(rev, got, versions, count, err)
		}
	}

	expect(do(disabled, "POST", auth, keyBody, "", origin, actor), 404, nil)
	expect(do(router, "POST", auth, keyBody, "", "https://foreign.invalid", actor), 403, nil)
	expect(do(router, "POST", auth, keyBody, "", origin, nil), 403, nil)
	wiki := issue(wikiAuth)
	docs := issue(docsAuth)
	edit := issue(auth)
	read := issue("/api/v1/auth/mcp-token")
	submit(read.Token, body(1, 1, 1, "Text after"), 401)
	submit(wiki.Token, body(1, 1, 1, "Text after"), 401)
	submit(docs.Token, body(1, 1, 1, "Text after"), 401)
	expect(do(router, "POST", "/api/v1/mcp/entities/1/outlets/1/wiki-create-proposals",
		`{"request_id":"88888888-1111-4333-8444-000000000001","title":"Fixture","body":"Not allowed"}`, edit.Token, "", nil), 401, nil)
	expect(do(router, "POST", "/api/v1/mcp/entities/1/outlets/1/document-draft-proposals",
		fmt.Sprintf(`{"request_id":"88888888-1111-4333-8444-000000000001","draft":%s}`, proposalInvoiceFixture), edit.Token, "", nil), 401, nil)
	expect(do(router, "GET", "/api/v1/mcp/entities", "", edit.Token, "", nil), 401, nil)
	expect(do(router, "POST", strings.Replace(bearer, "outlets/1", "outlets/2", 1), body(1, 1, 1, "Text after"), edit.Token, "", nil), 404, nil)
	submit(edit.Token, body(1, 2, 1, "Wrong article"), 404)
	p := submit(edit.Token, body(1, 1, 1, "Text after"), 201)
	if same := submit(edit.Token, body(1, 1, 1, "Text after"), 200); same.ID != p.ID {
		t.Fatal(same)
	}
	submit(edit.Token, body(1, 1, 1, "Different"), 409)
	check(1, "Text before")
	expect(do(router, "GET", item(p.ID), "", "", origin, other), 404, nil)
	expect(do(router, "GET", item(p.ID), "", "", "https://foreign.invalid", actor), 403, nil)
	d := detail(p.ID)
	if d.Edit == nil || d.Edit.ArticleID != 1 || d.Edit.Version != 1 ||
		d.Edit.Before != (wikiEditText{"Original", "Text before"}) || d.Edit.After != (wikiEditText{"Reviewed", "Text after"}) || len(d.Digest) != 64 {
		t.Fatal(d)
	}
	var list struct{ Proposals []wikiEditWireReceipt }
	expect(do(router, "GET", browser, "", "", origin, actor), 200, &list)
	if len(list.Proposals) != 1 || list.Proposals[0].ID != p.ID || list.Proposals[0].ArticleID != nil {
		t.Fatal(list)
	}
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", false), "", origin, actor), 422, nil)
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", true), edit.Token, origin, nil), 403, nil)
	decide(p.ID, strings.Repeat("0", 64), "approve", 409)
	exec("ALTER TABLE mcp_wiki_edit_proposals ADD CONSTRAINT fixture_edit_terminal_fault CHECK(status<>'approved') NOT VALID")
	decide(p.ID, d.Digest, "approve", 500)
	check(1, "Text before")
	if pending := detail(p.ID); pending.Status != "pending" || pending.Edit == nil {
		t.Fatal(pending)
	}
	exec("ALTER TABLE mcp_wiki_edit_proposals DROP CONSTRAINT fixture_edit_terminal_fault")
	expect(do(router, "PUT", "/api/v1/entities/1/outlets/1/knowledge/1",
		`{"titulo":"Browser edit","contenido":"Native text","version":1}`, "", origin, actor), 200, nil)
	decide(p.ID, d.Digest, "approve", 409)
	if same := submit(edit.Token, body(1, 1, 1, "Text after"), 200); same.ID != p.ID {
		t.Fatal(same)
	}
	decide(p.ID, d.Digest, "reject", 200)
	check(2, "Native text")
	p = submit(edit.Token, body(2, 1, 2, "Text after"), 201)
	d = detail(p.ID)
	exec("UPDATE memberships SET role='read_only' WHERE id=1")
	decide(p.ID, d.Digest, "approve", 403)
	exec("UPDATE memberships SET role='administration' WHERE id=1")

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
	check(3, "Text after")
	approved := detail(p.ID)
	if approved.Status != "approved" || approved.Edit != nil || approved.Digest != "" ||
		approved.ArticleID == nil || *approved.ArticleID != 1 || approved.ArticleVersion == nil || *approved.ArticleVersion != 3 {
		t.Fatal(approved)
	}
	if same := submit(edit.Token, body(2, 1, 2, "Text after"), 200); same.Status != "approved" || same.ArticleID != nil || same.ArticleVersion != nil {
		t.Fatal(same)
	}

	p = submit(edit.Token, body(3, 1, 3, "More"), 201)
	d = detail(p.ID)
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", wikiAuth, edit.ID), "", "", origin, actor), 204, nil)
	if got := detail(p.ID); got.Status != "pending" {
		t.Fatal(got)
	}
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", auth, edit.ID), "", "", origin, actor), 204, nil)
	decide(p.ID, d.Digest, "approve", 410)
	if got := detail(p.ID); got.Status != "revoked" || got.Edit != nil {
		t.Fatal(got)
	}

	next := issue(auth)
	p = submit(next.Token, body(4, 1, 3, "More"), 201)
	d = detail(p.ID)
	clock.Add(900)
	if got := detail(p.ID); got.Status != "expired" || got.Edit != nil {
		t.Fatal(got)
	}
	decide(p.ID, d.Digest, "approve", 410)
	if err := a.pruneWikiProposals(ctx); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_wiki_edit_proposals WHERE edit_preview IS NOT NULL").Scan(&pending); err != nil || pending != 0 {
		t.Fatal(pending, err)
	}
	check(3, "Text after")
}
