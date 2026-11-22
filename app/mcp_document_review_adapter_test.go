package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostgresDocumentReviewAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pw = "synthetic-review-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES(1,'review-fixture',$1,TRUE,FALSE,FALSE)`, encodePassword(pw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'accounting',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Synthetic supplier','30711111111',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at)
 VALUES(1,1,1,1,'30711111111','invoice','A',1,1,'2026-11-22','ARS',123456,'Test','draft',1,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com"}
	cookies := postgresTestCookies(t, a.routes(), "review-fixture", pw)
	req := func() *http.Request {
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://example.com/", nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		return r
	}
	scope := documentReviewScope{1, 1, 1}
	input := documentReviewInput{1, 1, "  Control de prueba  "}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := prepareDocumentReview(req(), tx, scope, input)
	if err != nil || p.Type != "invoice" || p.TotalMinor != 123456 || p.Input.Reason != "Control de prueba" {
		t.Fatal(p, err)
	}
	if _, err := prepareDocumentReview(req(), tx, documentReviewScope{1, 1, 2}, input); err == nil {
		t.Fatal("wrong outlet accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check := func(want string, revision, count int) {
		t.Helper()
		var status, kind string
		var rev, rows int
		var total int64
		if err := db.QueryRowContext(ctx, `SELECT status,revision,document_type,total_minor,(SELECT count(*) FROM document_audit WHERE document_id=1 AND action='review' AND reason='Control de prueba' AND actor_id=1) FROM documents WHERE id=1`).Scan(&status, &rev, &kind, &total, &rows); err != nil || status != want || rev != revision || kind != "invoice" || total != 123456 || rows != count {
			t.Fatal(status, rev, kind, total, rows, err)
		}
	}
	check("draft", 1, 0)
	apply := func(commit bool) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := a.applyDocumentReviewApproval(req(), tx, user{id: 1}, scope, p, p.digest()); err != nil {
			return err
		}
		if commit {
			return tx.Commit()
		}
		return nil
	}
	if err := apply(false); err != nil {
		t.Fatal(err)
	}
	check("draft", 1, 0)
	gate := make(chan struct{})
	outcomes := make(chan error, 2)
	for range 2 {
		go func() { <-gate; outcomes <- apply(true) }()
	}
	close(gate)
	success, stale := 0, 0
	for range 2 {
		switch err := <-outcomes; err {
		case nil:
			success++
		case draftRevisionConflict:
			stale++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatal(success, stale)
	}
	check("reviewed", 2, 1)
	var payable int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ap_payables WHERE source_document_id=1`).Scan(&payable); err != nil || payable != 0 {
		t.Fatal(payable, err)
	}
}
