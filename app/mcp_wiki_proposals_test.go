package main

import (
	"database/sql"
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

func TestWikiProposalRejectsAmbiguousInput(t *testing.T) {
	var input struct {
		Title string `json:"title"`
	}
	for _, raw := range []string{`{"title":"x","title":"y"}`, `{"title":"x","Title":"y"}`, `null`, `{}`, `{"title":7}`} {
		if err := decodeWikiProposalJSON([]byte(raw), &input, "title"); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if err := decodeWikiProposalJSON([]byte(`{"title":" x "}`), &input, "title"); err != nil {
		t.Fatal(err)
	}
	scope := wikiProposalScope{1, 2, 3}
	title, err := normalizeWikiProposal(input.Title, "body ")
	if err != nil || title != "x" {
		t.Fatal(title, err)
	}
	if wikiProposalDigest(scope, title, "body ") == wikiProposalDigest(scope, title, "body") ||
		wikiProposalDigest(scope, title, "body ") == wikiProposalDigest(wikiProposalScope{1, 2, 4}, title, "body ") {
		t.Fatal("digest does not bind exact body and scope")
	}
	for _, body := range []string{" ", "x\x00", strings.Repeat("字", 12001)} {
		if _, err := normalizeWikiProposal("title", body); err == nil {
			t.Fatal("accepted invalid body")
		}
	}
}

func TestPostgresWikiProposalApprovalAndCredentialBoundaries(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-wiki-test"
	encoded := encodePassword(password)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
	VALUES(1,'maker',$1,TRUE,FALSE,FALSE),(2,'other-admin',$1,TRUE,FALSE,FALSE),(3,'read-only',$1,TRUE,FALSE,FALSE)`, encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
	INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Other','00000000002',TRUE);
	INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Second',TRUE),(3,2,'Foreign',TRUE);
	INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),
		(2,2,1,'administration',TRUE),(3,3,1,'read_only',TRUE);
	INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1),(3,1)`); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC).Unix())
	a := &app{db: db, host: "example.com", enableMCP: true, enableMCPWikiProposals: true,
		mcpClock: func() time.Time { return time.Unix(clock.Load(), 0) }}
	router := a.routes()
	disabled := (&app{db: db, host: "example.com", enableMCP: true}).routes()
	maker := postgresTestCookies(t, router, "maker", password)
	other := postgresTestCookies(t, router, "other-admin", password)
	readOnly := postgresTestCookies(t, router, "read-only", password)

	const origin = "http://example.com"
	const auth = "/api/v1/auth/mcp-proposal-token"
	const browser = "/api/v1/entities/1/outlets/1/wiki-create-proposals"
	const proposalAPI = "/api/v1/mcp/entities/1/outlets/1/wiki-create-proposals"
	do := func(h http.Handler, method, path, body, bearer, requestOrigin string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body)).WithContext(ctx)
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
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
		if w.Header().Get("Cache-Control") != "no-store" && status != 404 {
			t.Fatal("missing no-store")
		}
		if dst != nil && json.Unmarshal(w.Body.Bytes(), dst) != nil {
			t.Fatal("invalid response JSON")
		}
	}
	type key struct {
		ID    int64
		Token string
	}
	issue := func() key {
		t.Helper()
		var v key
		expect(do(router, "POST", auth, `{"entity_id":1,"outlet_id":1}`, "", origin, maker), 201, &v)
		return v
	}
	submission := func(n int, title, body string) string {
		raw, err := json.Marshal(map[string]string{"request_id": fmt.Sprintf("88888888-1111-4333-8444-%012d", n), "title": title, "body": body})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	post := func(token string, n int, title, body string, want int) wikiProposalReceipt {
		t.Helper()
		w := do(router, "POST", proposalAPI, submission(n, title, body), token, "", nil)
		if strings.Contains(w.Body.String(), "Fixture draft") || strings.Contains(w.Body.String(), `"digest"`) {
			t.Fatal("agent response leaked preview")
		}
		var p wikiProposalReceipt
		expect(w, want, &p)
		return p
	}
	detailURL := func(id int64) string { return fmt.Sprintf("%s/%d", browser, id) }
	detail := func(id int64, who []*http.Cookie, want int) wikiProposalDetails {
		t.Helper()
		var out wikiProposalDetails
		expect(do(router, "GET", detailURL(id), "", "", origin, who), want, &out)
		return out
	}
	decisionBody := func(decision, digest string, confirm bool) string {
		return fmt.Sprintf(`{"decision":%q,"digest":%q,"confirm":%t}`, decision, digest, confirm)
	}
	decision := func(id int64, decision, digest string, confirm bool, want int) wikiProposalReceipt {
		t.Helper()
		var res wikiProposalReceipt
		expect(do(router, "POST", detailURL(id)+"/decision", decisionBody(decision, digest, confirm), "", origin, maker), want, &res)
		return res
	}
	count := func(articleCount, versionCount int) {
		t.Helper()
		var a, v int
		err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM knowledge_articles),(SELECT COUNT(*) FROM knowledge_article_versions)`).Scan(&a, &v)
		if err != nil || a != articleCount || v != versionCount {
			t.Fatalf("found articles=%d versions=%d: %v", a, v, err)
		}
	}
	expect(do(disabled, "POST", auth, `{"entity_id":1,"outlet_id":1}`, "", origin, maker), 404, nil)
	expect(do(router, "POST", auth, `{"entity_id":1,"outlet_id":1}`, "", "https://attacker.invalid", maker), 403, nil)
	expect(do(router, "POST", auth, `{"entity_id":1,"outlet_id":1}`, "", "", maker), 403, nil)
	expect(do(router, "POST", auth, `{"entity_id":1,"outlet_id":1}`, "", origin, readOnly), 403, nil)
	token := issue()
	readToken := key{}
	expect(do(router, "POST", "/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, "", origin, maker), 201, &readToken)
	expect(do(router, "POST", proposalAPI, submission(1, "Fixture draft", "synthetic text"), readToken.Token, "", nil), 401, nil)
	expect(do(router, "GET", "/api/v1/mcp/entities", "", token.Token, "", nil), 401, nil)
	expect(do(router, "GET", browser, "", token.Token, origin, nil), 401, nil)
	expect(do(router, "POST", strings.Replace(proposalAPI, "outlets/1", "outlets/2", 1), submission(1, "Fixture draft", "synthetic text"), token.Token, "", nil), 404, nil)
	p := post(token.Token, 1, " Fixture draft ", "synthetic text ", 201)
	if p.Status != "pending" || p.ID <= 0 || p.Expires != time.Unix(clock.Load()+900, 0).UTC().Format(time.RFC3339) {
		t.Fatal(p)
	}
	if got := post(token.Token, 1, "Fixture draft", "synthetic text ", 200); got.ID != p.ID {
		t.Fatal(got)
	}
	post(token.Token, 1, "Fixture draft", "other text", 409)
	count(0, 0)
	detail(p.ID, other, 404)
	expect(do(router, "GET", detailURL(p.ID), "", "", "https://attacker.invalid", maker), 403, nil)
	expect(do(router, "GET", detailURL(p.ID), "", "", "", maker), 403, nil)
	d := detail(p.ID, maker, 200)
	if d.Title == nil || *d.Title != "Fixture draft" || d.Body == nil || *d.Body != "synthetic text " || len(d.Digest) != 64 {
		t.Fatal(d)
	}
	decision(p.ID, "approve", d.Digest, false, 422)
	decision(p.ID, "approve", strings.Repeat("0", 64), true, 409)
	expect(do(router, "POST", detailURL(p.ID)+"/decision", decisionBody("approve", d.Digest, true), token.Token, origin, nil), 403, nil)
	count(0, 0)

	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			codes <- do(router, "POST", detailURL(p.ID)+"/decision", decisionBody("approve", d.Digest, true), "", origin, maker).Code
		})
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[200] != 1 || seen[409] != 1 {
		t.Fatal(seen)
	}
	count(1, 1)
	d = detail(p.ID, maker, 200)
	if d.Status != "approved" || d.Title != nil || d.Body != nil || d.Digest != "" || d.ArticleID == nil {
		t.Fatal(d)
	}
	var actor int64
	if err := db.QueryRowContext(ctx, "SELECT actor_id FROM knowledge_article_versions WHERE article_id=$1 AND version=1", *d.ArticleID).Scan(&actor); err != nil || actor != 1 {
		t.Fatal(actor, err)
	}
	if r := post(token.Token, 1, "Fixture draft", "synthetic text ", 200); r.Status != "approved" || r.ArticleID != nil {
		t.Fatal(r)
	}

	p = post(token.Token, 2, "Rejected fixture", "synthetic text", 201)
	d = detail(p.ID, maker, 200)
	r := decision(p.ID, "reject", d.Digest, true, 200)
	if r.Status != "rejected" || r.ArticleID != nil {
		t.Fatal(r)
	}
	count(1, 1)

	atomic := post(token.Token, 20, "Rollback fixture", "synthetic text", 201)
	preview := detail(atomic.ID, maker, 200)
	if _, err := db.ExecContext(ctx, "ALTER TABLE knowledge_article_versions ADD CONSTRAINT test_version_failure CHECK (article_id<0) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	decision(atomic.ID, "approve", preview.Digest, true, 500)
	if _, err := db.ExecContext(ctx, "ALTER TABLE knowledge_article_versions DROP CONSTRAINT test_version_failure"); err != nil {
		t.Fatal(err)
	}
	count(1, 1)
	if state := detail(atomic.ID, maker, 200); state.Status != "pending" || state.Body == nil {
		t.Fatal(state)
	}
	decision(atomic.ID, "reject", preview.Digest, true, 200)

	p = post(token.Token, 3, "Revoked fixture", "synthetic text", 201)
	d = detail(p.ID, maker, 200)
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", auth, token.ID), "", "", origin, other), 204, nil)
	if v := detail(p.ID, maker, 200); v.Status != "pending" {
		t.Fatal(v)
	}
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", auth, token.ID), "", "", origin, maker), 204, nil)
	decision(p.ID, "approve", d.Digest, true, 410)
	if v := detail(p.ID, maker, 200); v.Status != "revoked" || v.Body != nil {
		t.Fatal(v)
	}
	post(token.Token, 4, "Post-revocation", "synthetic text", 401)

	token = issue()
	p = post(token.Token, 4, "Expired fixture", "synthetic text", 201)
	d = detail(p.ID, maker, 200)
	if _, err := db.ExecContext(ctx, "DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1"); err != nil {
		t.Fatal(err)
	}
	decision(p.ID, "approve", d.Digest, true, 404)
	if _, err := db.ExecContext(ctx, "INSERT INTO outlet_grants VALUES(1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='read_only' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	decision(p.ID, "approve", d.Digest, true, 403)
	if _, err := db.ExecContext(ctx, "UPDATE memberships SET role='administration' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	clock.Add(900)
	post(token.Token, 5, "Post-expiry", "synthetic text", 401)
	if v := detail(p.ID, maker, 200); v.Status != "expired" || v.Body != nil || v.Digest != "" {
		t.Fatal(v)
	}
	if err := a.pruneWikiProposals(ctx); err != nil {
		t.Fatal(err)
	}
	decision(p.ID, "approve", d.Digest, true, 410)
	var retained sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT body FROM mcp_wiki_proposals WHERE id=$1", p.ID).Scan(&retained); err != nil || retained.Valid {
		t.Fatal(retained, err)
	}
	clock.Add(86400)
	if err := a.pruneWikiProposals(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT body FROM mcp_wiki_proposals WHERE id=$1", p.ID).Scan(&retained); err != sql.ErrNoRows {
		t.Fatal(err)
	}
	count(1, 1)
}
