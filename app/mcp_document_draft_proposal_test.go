package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const proposalInvoiceFixture = `{"supplier_id":1,"type":"invoice","letter":" a ","point_of_sale":3,"number":40,
	"issue_date":"2026-10-05","due_date":"2026-11-05","currency":"ARS","net_minor":10010,"tax_minor":2102,"total_minor":12112}`

func TestDocumentDraftProposalTypedInput(t *testing.T) {
	valid, err := decodeDocumentDraftProposal([]byte(proposalInvoiceFixture))
	if err != nil || valid.Letter != "A" || *valid.Total != 12112 || *valid.DueDate != "2026-11-05" {
		t.Fatal(valid, err)
	}
	for _, raw := range []string{
		strings.Replace(proposalInvoiceFixture, `"supplier_id":1`, `"supplier_id":1,"supplier_id":2`, 1),
		strings.Replace(proposalInvoiceFixture, `"supplier_id":1`, `"Supplier_ID":1`, 1),
		strings.Replace(proposalInvoiceFixture, `"supplier_id":1`, `"supplier_id":1,"issuer_cuit":"00000000000"`, 1),
		strings.Replace(proposalInvoiceFixture, `"supplier_id":1`, `"supplier_id":1,"outlet_id":3`, 1),
		strings.Replace(proposalInvoiceFixture, `"supplier_id":1`, `"supplier_id":1,"file":"not accepted"`, 1),
		strings.Replace(proposalInvoiceFixture, `"supplier_id":1`, `"supplier_id":1,"reviewed":true`, 1),
		strings.Replace(proposalInvoiceFixture, `"supplier_id":1`, `"supplier_id":1,"description":"not accepted"`, 1),
		strings.Replace(proposalInvoiceFixture, "12112", "12112.0", 1),
		strings.Replace(proposalInvoiceFixture, "12112", "-1", 1),
		strings.Replace(proposalInvoiceFixture, "12112", "100000000000000", 1),
		strings.Replace(proposalInvoiceFixture, "12112", "null", 1),
		strings.Replace(proposalInvoiceFixture, "2026-11-05", "2026-11-31", 1),
		strings.Replace(proposalInvoiceFixture, "2026-10-05", "0000-10-05", 1),
		strings.Replace(proposalInvoiceFixture, "ARS", "EUR", 1),
		"null",
	} {
		if _, err := decodeDocumentDraftProposal([]byte(raw)); err == nil {
			t.Errorf("accepted invalid proposal: %s", raw)
		}
	}
	upper := strings.Replace(proposalInvoiceFixture, "12112", "99999999999999", 1)
	max, err := decodeDocumentDraftProposal([]byte(upper))
	if err != nil || *buildDocumentProposalDraft(documentDraftProposalScope{1, 1, 1}, max, "30711111111").Total != "999999999999.99" {
		t.Fatal(max, err)
	}
}

func TestPostgresDocumentDraftProposalAdapter(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic-draft-proposal"
	encoded := encodePassword(pass)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser) VALUES
		(1,'actor-one',$1,TRUE,FALSE,FALSE),(2,'actor-two',$1,TRUE,FALSE,FALSE),(3,'reader',$1,TRUE,FALSE,FALSE)`, encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO entities(id,name,cuit,active) VALUES(1,'Synthetic','30111111118',TRUE),(2,'Foreign','30111111119',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'First',TRUE),(2,1,'Second',TRUE),(3,2,'Foreign',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'purchasing',TRUE),(3,3,1,'read_only',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(2,1),(3,1);
		INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Synthetic supplier','30711111111',TRUE),
			(2,1,'Inactive supplier','30711111112',FALSE),(3,2,'Foreign supplier','30711111111',TRUE)`); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com"}
	router := a.routes()
	cookies := postgresTestCookies(t, router, "actor-one", pass)
	request := func(path, body string, authenticated bool) *http.Request {
		r := httptest.NewRequest("POST", "http://example.com"+path, strings.NewReader(body)).WithContext(ctx)
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
	input, err := decodeDocumentDraftProposal([]byte(proposalInvoiceFixture))
	if err != nil {
		t.Fatal(err)
	}
	scope := documentDraftProposalScope{1, 1, 1}
	prepare := func(s documentDraftProposalScope, in documentDraftProposalInput) (documentDraftProposal, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		p, err := prepareDocumentDraftProposal(ctx, tx, s, in)
		if err != nil {
			return p, err
		}
		return p, tx.Commit()
	}
	expectPrepare := func(s documentDraftProposalScope, in documentDraftProposalInput, want error) {
		t.Helper()
		if _, got := prepare(s, in); got != want {
			t.Fatalf("prepare returned %v, want %v", got, want)
		}
	}
	expectPrepare(documentDraftProposalScope{1, 1, 2}, input, missingOutlet)
	expectPrepare(documentDraftProposalScope{1, 1, 3}, input, missingOutlet)
	expectPrepare(documentDraftProposalScope{3, 1, 1}, input, forbiddenRole)
	for _, id := range []int64{2, 3, 4} {
		bad := input
		bad.SupplierID = id
		expectPrepare(scope, bad, draftProposalSupplierMissing)
	}
	// Purchasing remains a native draft role. The outer protocol can be
	// narrower when issuing keys. It cannot widen these checks.
	expectPrepare(documentDraftProposalScope{2, 1, 1}, input, nil)
	p, err := prepare(scope, input)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := json.Marshal(p.Preview)
	if err != nil || !strings.Contains(string(preview), `"supplier_name":"Synthetic supplier"`) ||
		!strings.Contains(string(preview), `"total_minor":12112`) || strings.Contains(string(preview), "30711111111") {
		t.Fatal(string(preview), err)
	}
	count := func(documents, audit, payables, payments, attachments int) {
		t.Helper()
		var d, au, ap, py, att int
		err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM documents),(SELECT count(*) FROM document_audit),
			(SELECT count(*) FROM ap_payables),(SELECT count(*) FROM ap_payments),(SELECT count(*) FROM document_attachments)`).
			Scan(&d, &au, &ap, &py, &att)
		if err != nil || d != documents || au != audit || ap != payables || py != payments || att != attachments {
			t.Fatalf("documents/audit/payables/payments/attachments: %d/%d/%d/%d/%d, %v", d, au, ap, py, att, err)
		}
	}
	apply := func(s documentDraftProposalScope, proposal documentDraftProposal, digest [32]byte, authenticated bool) (int64, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		id, err := a.applyDocumentDraftProposal(request("/synthetic/decision", "", authenticated), tx, user{id: 1}, s, proposal, digest)
		if err != nil {
			return 0, err
		}
		return id, tx.Commit()
	}
	expectApply := func(s documentDraftProposalScope, proposal documentDraftProposal, digest [32]byte, authenticated bool, want error) {
		t.Helper()
		if _, got := apply(s, proposal, digest, authenticated); got != want {
			t.Fatalf("apply returned %v, want %v", got, want)
		}
	}
	count(0, 0, 0, 0, 0)
	digest := p.digest()
	expectApply(scope, p, [32]byte{}, true, draftProposalStale)
	expectApply(documentDraftProposalScope{1, 1, 2}, p, digest, true, draftProposalStale)
	expectApply(documentDraftProposalScope{2, 1, 1}, p, digest, true, unauthenticated)
	expectApply(scope, p, digest, false, unauthenticated)

	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE suppliers SET name='Changed supplier' WHERE id=1")
	expectApply(scope, p, digest, true, draftProposalStale)
	exec("UPDATE suppliers SET name='Synthetic supplier',active=FALSE WHERE id=1")
	expectApply(scope, p, digest, true, draftProposalStale)
	exec("UPDATE suppliers SET active=TRUE,cuit='30711111113' WHERE id=1")
	expectApply(scope, p, digest, true, draftProposalStale)
	exec("UPDATE suppliers SET cuit='30711111111' WHERE id=1")
	exec("DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1")
	expectApply(scope, p, digest, true, missingOutlet)
	exec("INSERT INTO outlet_grants VALUES(1,1)")
	exec("UPDATE memberships SET role='read_only' WHERE id=1")
	expectApply(scope, p, digest, true, forbiddenRole)
	exec("UPDATE memberships SET role='administration' WHERE id=1")
	exec("ALTER TABLE document_audit ADD CONSTRAINT test_draft_proposal_audit_fault CHECK(document_id<0) NOT VALID")
	if _, err := apply(scope, p, digest, true); err == nil {
		t.Fatal("audit failure did not abort draft")
	}
	count(0, 0, 0, 0, 0)
	exec("ALTER TABLE document_audit DROP CONSTRAINT test_draft_proposal_audit_fault")

	type decision struct {
		id  int64
		err error
	}
	done := make(chan decision, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			id, err := apply(scope, p, digest, true)
			done <- decision{id, err}
		})
	}
	wg.Wait()
	close(done)
	var created, duplicates int
	var document int64
	for result := range done {
		switch result.err {
		case nil:
			created++
			document = result.id
		case duplicateDocument:
			duplicates++
		default:
			t.Fatal(result.err)
		}
	}
	if created != 1 || duplicates != 1 || document <= 0 {
		t.Fatalf("got %d created, %d duplicate", created, duplicates)
	}
	count(1, 1, 0, 0, 0)
	var status, reason string
	var revision, outlet, vendor, net, tax, total, actor int64
	if err := db.QueryRowContext(ctx, `SELECT d.status,d.revision,d.outlet_id,d.supplier_id,d.subtotal_minor,d.tax_minor,d.total_minor,a.actor_id,a.reason
		FROM documents d JOIN document_audit a ON a.document_id=d.id AND a.action='create' WHERE d.id=$1`, document).
		Scan(&status, &revision, &outlet, &vendor, &net, &tax, &total, &actor, &reason); err != nil || status != "draft" || revision != 1 ||
		outlet != 1 || vendor != 1 || net != 10010 || tax != 2102 || total != 12112 || actor != 1 || reason != documentDraftProposalReason {
		t.Fatal(status, revision, outlet, vendor, net, tax, total, actor, reason, err)
	}
	invoke := func(suffix, body string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request(fmt.Sprintf("/api/v1/entities/1/documents/%d/%s", document, suffix), body, true))
		if w.Code != want {
			t.Fatalf("%s: got %d, want %d: %s", suffix, w.Code, want, w.Body.String())
		}
	}
	invoke("payable", `{"motivo":"No registrar un borrador"}`, 422)
	invoke("review", `{"motivo":"Verificación manual","expected_revision":1}`, 200)
	invoke("review", `{"motivo":"Reintento","expected_revision":1}`, 409)
	count(1, 2, 0, 0, 0)
	var reviewed sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT status FROM documents WHERE id=$1 AND revision=2", document).Scan(&reviewed); err != nil || reviewed.String != "reviewed" {
		t.Fatal(reviewed, err)
	}
}
