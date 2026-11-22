package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestWikiEditTypedProposal(t *testing.T) {
	const fixture = `{"article_id":1,"expected_version":2,"title":" Receta ","body":"<b>Texto literal</b>\n"}`
	got, err := decodeWikiEditProposal([]byte(fixture))
	if err != nil || got.Title != "Receta" || got.Body != "<b>Texto literal</b>\n" {
		t.Fatal(got, err)
	}
	for _, bad := range []string{
		strings.Replace(fixture, `"article_id":1`, `"article_id":1,"article_id":2`, 1),
		strings.Replace(fixture, `"article_id":1`, `"Article_ID":1`, 1),
		strings.Replace(fixture, `"article_id":1`, `"article_id":1,"outlet_id":2`, 1),
		strings.Replace(fixture, `"article_id":1`, `"article_id":1,"delete":true`, 1),
		strings.Replace(fixture, `"expected_version":2`, `"expected_version":9223372036854775807`, 1),
		strings.Replace(fixture, `"expected_version":2`, `"expected_version":null`, 1),
		strings.Replace(fixture, `"article_id":1`, `"article_id":0`, 1),
		strings.Replace(fixture, `"body":"<b>Texto literal</b>\n"`, `"body":" "`, 1),
		"null",
	} {
		if _, err := decodeWikiEditProposal([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	scope := wikiEditScope{Actor: 1, Entity: 2, Outlet: 3}
	p := wikiEditProposal{scope, wikiEditPreview{1, 2, wikiEditText{"Original", "Original text"}, wikiEditText{got.Title, got.Body}}}
	for name, change := range map[string]func(*wikiEditProposal){
		"actor":   func(q *wikiEditProposal) { q.Scope.Actor++ },
		"entity":  func(q *wikiEditProposal) { q.Scope.Entity++ },
		"outlet":  func(q *wikiEditProposal) { q.Scope.Outlet++ },
		"article": func(q *wikiEditProposal) { q.Preview.ArticleID++ },
		"version": func(q *wikiEditProposal) { q.Preview.Version++ },
		"before":  func(q *wikiEditProposal) { q.Preview.Before.Body += "!" },
		"after":   func(q *wikiEditProposal) { q.Preview.After.Body += "!" },
	} {
		copy := p
		change(&copy)
		if copy.digest() == p.digest() {
			t.Errorf("digest did not bind %s", name)
		}
	}
}

func TestPostgresWikiEditProposalAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-wiki-edit-test"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'edit-admin',$1,TRUE,FALSE,FALSE),(2,'edit-buyer',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','00000000001',TRUE),(2,'Foreign','00000000002',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Another grant',TRUE),(3,2,'Foreign',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'purchasing',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1);
		INSERT INTO knowledge_articles(id,outlet_id,title,body,search_text,version,created_at,updated_at)
		  VALUES(1,1,'Original','Original text','original',1,'time','time'),(2,2,'Other','Other text','other',1,'time','time');
		INSERT INTO knowledge_article_versions(article_id,version,actor_id,recorded_at) VALUES(1,1,1,'time'),(2,1,1,'time');`)
	a := &app{db: db, host: "example.com"}
	router := a.routes()
	cookies := postgresTestCookies(t, router, "edit-admin", password)
	request := func(method, path, body string, authenticated bool) *http.Request {
		r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		return r
	}
	scope := wikiEditScope{1, 1, 1}
	input := wikiEditInput{1, 1, " New title ", "<script>Literal</script>"}
	prepare := func(s wikiEditScope, in wikiEditInput) (wikiEditProposal, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		p, err := prepareWikiEditProposal(ctx, tx, s, in)
		if err != nil {
			return p, err
		}
		return p, tx.Commit()
	}
	expectPrepare := func(s wikiEditScope, input wikiEditInput, want error) {
		t.Helper()
		if _, got := prepare(s, input); got != want {
			t.Fatalf("prepare got %v, want %v", got, want)
		}
	}
	expectPrepare(wikiEditScope{1, 1, 2}, input, missingKnowledge)
	expectPrepare(wikiEditScope{1, 1, 3}, input, missingOutlet)
	expectPrepare(wikiEditScope{2, 1, 1}, input, forbiddenRole)
	expectPrepare(scope, wikiEditInput{1, 2, "New title", "New text"}, staleKnowledge)
	expectPrepare(scope, wikiEditInput{1, 1, "Original", "Original text"}, wikiEditNoChange)
	p, err := prepare(scope, input)
	if err != nil || p.Preview.Before != (wikiEditText{"Original", "Original text"}) || p.Preview.After != (wikiEditText{"New title", "<script>Literal</script>"}) {
		t.Fatal(p, err)
	}
	const destination = "/api/v1/entities/1/outlets/1/knowledge/1"
	apply := func(s wikiEditScope, p wikiEditProposal, digest [32]byte, authenticated bool) (knowledgeArticle, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return knowledgeArticle{}, err
		}
		defer tx.Rollback()
		result, err := a.applyWikiEditProposal(request("POST", "/synthetic/decision", "", authenticated), tx, user{id: 1}, s, p, digest)
		if err != nil {
			return knowledgeArticle{}, err
		}
		return result, tx.Commit()
	}
	expectApply := func(s wikiEditScope, p wikiEditProposal, digest [32]byte, authenticated bool, want error) {
		t.Helper()
		if _, got := apply(s, p, digest, authenticated); got != want {
			t.Fatalf("apply got %v, want %v", got, want)
		}
	}
	assert := func(version int64, title, body string, versions int) {
		t.Helper()
		var revision int64
		var gotTitle, gotBody string
		var n int
		err := db.QueryRowContext(ctx, `SELECT version,title,body,(SELECT count(*) FROM knowledge_article_versions WHERE article_id=1)
			FROM knowledge_articles WHERE id=1`).Scan(&revision, &gotTitle, &gotBody, &n)
		if err != nil || revision != version || gotTitle != title || gotBody != body || n != versions {
			t.Fatal(revision, gotTitle, gotBody, n, err)
		}
	}
	assert(1, "Original", "Original text", 1)
	expectApply(scope, p, [32]byte{}, true, staleKnowledge)
	expectApply(wikiEditScope{1, 1, 2}, p, p.digest(), true, staleKnowledge)
	expectApply(wikiEditScope{2, 1, 1}, p, p.digest(), true, unauthenticated)
	expectApply(scope, p, p.digest(), false, unauthenticated)
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1")
	expectApply(scope, p, p.digest(), true, missingOutlet)
	exec("INSERT INTO outlet_grants VALUES(1,1)")
	exec("UPDATE memberships SET role='purchasing' WHERE id=1")
	expectApply(scope, p, p.digest(), true, forbiddenRole)
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	exec("ALTER TABLE knowledge_article_versions ADD CONSTRAINT test_edit_fault CHECK(version<2) NOT VALID")
	if _, err := apply(scope, p, p.digest(), true); err == nil {
		t.Fatal("audit failure did not abort")
	}
	assert(1, "Original", "Original text", 1)
	exec("ALTER TABLE knowledge_article_versions DROP CONSTRAINT test_edit_fault")

	// Establish a stale preview with a concurrent native browser writer.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, request(http.MethodPut, destination, `{"titulo":"Native edit","contenido":"Browser text","version":1}`, true))
	if w.Code != 200 {
		t.Fatalf("native edit failed: %d %s", w.Code, w.Body.String())
	}
	expectApply(scope, p, p.digest(), true, staleKnowledge)
	assert(2, "Native edit", "Browser text", 2)
	input.Version = 2
	p, err = prepare(scope, input)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := apply(scope, p, p.digest(), true); results <- err })
	}
	wg.Wait()
	close(results)
	var applied, stale int
	for err := range results {
		switch err {
		case nil:
			applied++
		case staleKnowledge:
			stale++
		default:
			t.Fatal(err)
		}
	}
	if applied != 1 || stale != 1 {
		t.Fatal(applied, stale)
	}
	assert(3, "New title", "<script>Literal</script>", 3)
	var auditActor sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT actor_id FROM knowledge_article_versions WHERE article_id=1 AND version=3").Scan(&auditActor); err != nil || auditActor.Int64 != 1 {
		t.Fatal(auditActor, err)
	}
}
