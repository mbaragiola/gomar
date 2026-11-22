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

func TestPostgresMCPDocumentProposalBoundaries(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-doc-proposal"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'approver',$1,TRUE,FALSE,FALSE),(2,'other',$1,TRUE,FALSE,FALSE),(3,'purchase',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
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
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),
			(2,2,1,'administration',TRUE),(3,3,1,'purchasing',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1),(3,1);
		INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Synthetic supplier','30711111111',TRUE)`)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC).Unix())
	a := &app{db: db, host: "example.com", enableMCP: true, enableMCPWikiProposals: true, enableMCPDocumentProposals: true,
		mcpClock: func() time.Time { return time.Unix(clock.Load(), 0) }}
	router := a.routes()
	disabled := (&app{db: db, host: "example.com", enableMCP: true, enableMCPWikiProposals: true}).routes()
	actor := postgresTestCookies(t, router, "approver", password)
	other := postgresTestCookies(t, router, "other", password)
	buyer := postgresTestCookies(t, router, "purchase", password)
	const origin = "http://example.com"
	const auth = "/api/v1/auth/mcp-document-proposal-token"
	const wiki = "/api/v1/auth/mcp-proposal-token"
	const browser = "/api/v1/entities/1/outlets/1/document-draft-proposals"
	const bearer = "/api/v1/mcp/entities/1/outlets/1/document-draft-proposals"
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
		if k.ID <= 0 || len(k.Token) != 43 {
			t.Fatal("credential not issued")
		}
		return k
	}
	body := func(n int, draft string) string {
		return fmt.Sprintf(`{"request_id":"88888888-1111-4333-8444-%012d","draft":%s}`, n, draft)
	}
	submit := func(token string, n int, draft string, status int) documentProposalReceipt {
		t.Helper()
		w := do(router, "POST", bearer, body(n, draft), token, "", nil)
		var receipt documentProposalReceipt
		expect(w, status, &receipt)
		for _, private := range []string{"30711111111", "Synthetic supplier", "total_minor", "draft_preview", "digest", "document_id"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("agent receipt leaked %s", private)
			}
		}
		return receipt
	}
	item := func(id int64) string { return fmt.Sprintf("%s/%d", browser, id) }
	detail := func(id int64) documentProposalDetails {
		t.Helper()
		var d documentProposalDetails
		expect(do(router, "GET", item(id), "", "", origin, actor), 200, &d)
		return d
	}
	decisionBody := func(digest, verb string, confirm bool) string {
		return fmt.Sprintf(`{"decision":%q,"digest":%q,"confirm":%t}`, verb, digest, confirm)
	}
	decide := func(id int64, digest, verb string, want int) documentProposalReceipt {
		t.Helper()
		var receipt documentProposalReceipt
		expect(do(router, "POST", item(id)+"/decision", decisionBody(digest, verb, true), "", origin, actor), want, &receipt)
		return receipt
	}
	count := func(documents, audit int) {
		t.Helper()
		var n, m, p, q, f int
		err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM documents),(SELECT count(*) FROM document_audit),
			(SELECT count(*) FROM ap_payables),(SELECT count(*) FROM ap_payments),(SELECT count(*) FROM document_attachments)`).Scan(&n, &m, &p, &q, &f)
		if err != nil || n != documents || m != audit || p != 0 || q != 0 || f != 0 {
			t.Fatal(n, m, p, q, f, err)
		}
	}

	expect(do(disabled, "POST", auth, keyBody, "", origin, actor), 404, nil)
	expect(do(router, "POST", auth, keyBody, "", "https://foreign.invalid", actor), 403, nil)
	expect(do(router, "POST", auth, keyBody, "", origin, nil), 403, nil)
	expect(do(router, "POST", wiki, keyBody, "", origin, buyer), 403, nil)
	wikiKey := issue(wiki, actor)
	key1 := issue(auth, actor)
	buyerKey := issue(auth, buyer)
	buyerProposal := submit(buyerKey.Token, 1, proposalInvoiceFixture, 201)
	expect(do(router, "GET", item(buyerProposal.ID), "", "", origin, actor), 404, nil)
	readKey := issue("/api/v1/auth/mcp-token", actor)
	submit(readKey.Token, 1, proposalInvoiceFixture, 401)
	submit(wikiKey.Token, 1, proposalInvoiceFixture, 401)
	expect(do(router, "POST", "/api/v1/mcp/entities/1/outlets/1/wiki-create-proposals",
		`{"request_id":"88888888-1111-4333-8444-000000000001","title":"Fixture","body":"Not allowed"}`, key1.Token, "", nil), 401, nil)
	expect(do(router, "GET", "/api/v1/mcp/entities", "", key1.Token, "", nil), 401, nil)
	expect(do(router, "POST", strings.Replace(bearer, "outlets/1", "outlets/2", 1), body(1, proposalInvoiceFixture), key1.Token, "", nil), 404, nil)
	p := submit(key1.Token, 1, proposalInvoiceFixture, 201)
	if same := submit(key1.Token, 1, proposalInvoiceFixture, 200); same.ID != p.ID {
		t.Fatal(same)
	}
	submit(key1.Token, 1, strings.Replace(proposalInvoiceFixture, "12112", "12113", 1), 409)
	count(0, 0)
	expect(do(router, "GET", item(p.ID), "", "", origin, other), 404, nil)
	expect(do(router, "GET", browser, "", "", "https://foreign.invalid", actor), 403, nil)
	d := detail(p.ID)
	if d.Draft == nil || d.Draft.SupplierName != "Synthetic supplier" || *d.Draft.Total != 12112 || len(d.Digest) != 64 {
		t.Fatal(d)
	}
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", false), "", origin, actor), 422, nil)
	expect(do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", true), key1.Token, origin, nil), 403, nil)
	decide(p.ID, strings.Repeat("0", 64), "approve", 409)
	exec("UPDATE suppliers SET name='Changed supplier' WHERE id=1")
	if same := submit(key1.Token, 1, proposalInvoiceFixture, 200); same.ID != p.ID {
		t.Fatal(same)
	}
	decide(p.ID, d.Digest, "approve", 409)
	count(0, 0)
	exec("UPDATE suppliers SET name='Synthetic supplier' WHERE id=1")
	exec("UPDATE memberships SET role='read_only' WHERE id=1")
	decide(p.ID, d.Digest, "approve", 403)
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	exec("ALTER TABLE mcp_document_draft_proposals ADD CONSTRAINT fixture_terminal_failure CHECK(status<>'approved') NOT VALID")
	decide(p.ID, d.Digest, "approve", 500)
	count(0, 0)
	if still := detail(p.ID); still.Status != "pending" || still.Draft == nil {
		t.Fatal(still)
	}
	exec("ALTER TABLE mcp_document_draft_proposals DROP CONSTRAINT fixture_terminal_failure")

	var wg sync.WaitGroup
	result := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			result <- do(router, "POST", item(p.ID)+"/decision", decisionBody(d.Digest, "approve", true), "", origin, actor).Code
		})
	}
	wg.Wait()
	close(result)
	codes := map[int]int{}
	for status := range result {
		codes[status]++
	}
	if codes[200] != 1 || codes[409] != 1 {
		t.Fatal(codes)
	}
	count(1, 1)
	approved := detail(p.ID)
	if approved.Status != "approved" || approved.DocumentID == nil || approved.Draft != nil || approved.Digest != "" {
		t.Fatal(approved)
	}
	var state string
	var revision int
	if err := db.QueryRowContext(ctx, "SELECT status,revision FROM documents WHERE id=$1", *approved.DocumentID).Scan(&state, &revision); err != nil || state != "draft" || revision != 1 {
		t.Fatal(state, revision, err)
	}
	if same := submit(key1.Token, 1, proposalInvoiceFixture, 200); same.ID != p.ID || same.Status != "approved" {
		t.Fatal(same)
	}

	p = submit(key1.Token, 2, proposalInvoiceFixture, 201)
	d = detail(p.ID)
	decide(p.ID, d.Digest, "approve", 409)
	reject := decide(p.ID, d.Digest, "reject", 200)
	if reject.Status != "rejected" || reject.DocumentID != nil {
		t.Fatal(reject)
	}
	p = submit(key1.Token, 3, proposalInvoiceFixture, 201)
	d = detail(p.ID)
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", wiki, key1.ID), "", "", origin, actor), 204, nil)
	if got := detail(p.ID); got.Status != "pending" {
		t.Fatal(got)
	}
	expect(do(router, "DELETE", fmt.Sprintf("%s/%d", auth, key1.ID), "", "", origin, actor), 204, nil)
	decide(p.ID, d.Digest, "approve", 410)
	if got := detail(p.ID); got.Status != "revoked" || got.Draft != nil {
		t.Fatal(got)
	}

	next := issue(auth, actor)
	p = submit(next.Token, 4, proposalInvoiceFixture, 201)
	d = detail(p.ID)
	clock.Add(900)
	if got := detail(p.ID); got.Status != "expired" || got.Draft != nil {
		t.Fatal(got)
	}
	decide(p.ID, d.Digest, "approve", 410)
	if err := a.pruneWikiProposals(ctx); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM mcp_document_draft_proposals WHERE draft_preview IS NOT NULL").Scan(&pending); err != nil || pending != 0 {
		t.Fatal(pending, err)
	}
	count(1, 1)
}
