package main

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
)

const documentEditFixtureJSON = `{"document_id":1,"expected_revision":0,"reason":" Fecha corregida ","after":{"issue_date":"2026-11-21","due_date":"2026-11-29","net_minor":1000,"tax_minor":210,"total_minor":1210}}`

func TestDecodeDocumentEdit(t *testing.T) {
	in, err := decodeDocumentEdit([]byte(documentEditFixtureJSON))
	if err != nil || in.Revision != 0 || in.Reason != "Fecha corregida" || in.After.Total != 1210 {
		t.Fatal(in, err)
	}
	for _, body := range []string{
		strings.Replace(documentEditFixtureJSON, `"expected_revision":0,`, "", 1),
		strings.Replace(documentEditFixtureJSON, `"expected_revision":0`, `"expected_revision":null`, 1),
		strings.Replace(documentEditFixtureJSON, `"net_minor":1000`, `"net_minor":null`, 1),
		strings.Replace(documentEditFixtureJSON, `"total_minor":1210`, `"total_minor":1211`, 1),
		strings.Replace(documentEditFixtureJSON, `2026-11-29`, `2026-11-20`, 1),
		strings.Replace(documentEditFixtureJSON, `"total_minor":1210`, `"total_minor":1210,"supplier_id":2`, 1),
	} {
		if _, err := decodeDocumentEdit([]byte(body)); err == nil {
			t.Fatal("invalid edit accepted")
		}
	}
}

func TestPostgresMCPDocumentEditAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-document-edit-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'edit-fixture',$1,TRUE,FALSE,FALSE),(2,'reader-fixture','inert',TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'purchasing',TRUE),(2,2,1,'read_only',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1);
 INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Synthetic supplier','30711111111',TRUE);
 INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at)
 VALUES(1,1,1,1,'30711111111','invoice','A',1,8,'2026-11-22','ARS',999,'Untouched','draft',0,'2026-11-22T12:00:00Z','2026-11-22T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com"}
	r := httptest.NewRequestWithContext(ctx, "POST", "http://example.com/", nil)
	for _, c := range postgresTestCookies(t, a.routes(), "edit-fixture", password) {
		r.AddCookie(c)
	}
	in, err := decodeDocumentEdit([]byte(documentEditFixtureJSON))
	if err != nil {
		t.Fatal(err)
	}
	scope := documentReviewScope{1, 1, 1}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := prepareDocumentEdit(r, tx, scope, in)
	if err != nil || p.Before.Total != 999 || p.Before.DueDate != nil || p.Currency != "ARS" {
		t.Fatal(p, err)
	}
	for _, denied := range []documentReviewScope{{2, 1, 1}, {1, 1, 2}} {
		if _, err := prepareDocumentEdit(r, tx, denied, in); err == nil {
			t.Fatal("unavailable scope accepted")
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check := func(wantRevision, wantAmount, wantAudit int, wantIssue string) {
		t.Helper()
		var rev, amount, audits, payables int
		var issue string
		err := db.QueryRowContext(ctx, `SELECT revision,total_minor,issue_date,
 (SELECT count(*) FROM document_audit WHERE document_id=1 AND action='update' AND reason='Fecha corregida' AND actor_id=1),
 (SELECT count(*) FROM ap_payables WHERE source_document_id=1)
 FROM documents WHERE id=1 AND status='draft' AND outlet_id=1 AND supplier_id=1 AND issuer_cuit='30711111111'
 AND document_type='invoice' AND letter='A' AND point_of_sale=1 AND number=8 AND currency='ARS' AND description='Untouched'`).Scan(&rev, &amount, &issue, &audits, &payables)
		if err != nil || rev != wantRevision || amount != wantAmount || issue != wantIssue || audits != wantAudit || payables != 0 {
			t.Fatal(rev, amount, issue, audits, payables, err)
		}
	}
	apply := func(proposal documentEditPreview, commit bool) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = a.applyDocumentEditApproval(r, tx, user{id: 1}, scope, proposal, p.digest()); err != nil {
			return err
		}
		if commit {
			return tx.Commit()
		}
		return nil
	}
	changed := p
	changed.Before.Total++
	if err := apply(changed, false); err == nil {
		t.Fatal("changed snapshot accepted")
	}
	if err := apply(p, false); err != nil {
		t.Fatal(err)
	}
	check(0, 999, 0, "2026-11-22")
	if err := apply(p, true); err != nil {
		t.Fatal(err)
	}
	if err := apply(p, true); err != draftRevisionConflict {
		t.Fatal("expected stale revision, got", err)
	}
	check(1, 1210, 1, "2026-11-21")
	var due string
	var net, tax int
	if err := db.QueryRowContext(ctx, `SELECT due_date,subtotal_minor,tax_minor FROM documents WHERE id=1`).Scan(&due, &net, &tax); err != nil || due != "2026-11-29" || net != 1000 || tax != 210 {
		t.Fatal(due, net, tax, err)
	}
}
