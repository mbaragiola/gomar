package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	a := app{db: db, host: "example.com", enableMCPDocumentReviewProposals: true}
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
	if _, err := db.ExecContext(ctx, `INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at)
 SELECT 2,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,point_of_sale,2,issue_date,currency,total_minor,description,'draft',0,created_at,updated_at FROM documents WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	handler := a.routes()
	call := func(method, path, body, bearer string, login []*http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
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
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d", method, path, w.Code, want)
		}
		return w
	}
	tokenPath := proposalSessionsAuthPath
	res := call("POST", tokenPath, `{"entity_id":1,"outlet_id":1,"actions":["document_draft_review"]}`, "", cookies, 201)
	var minted struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &minted); err != nil || minted.ID < 1 || len(minted.Token) != 43 {
		t.Fatal("missing synthetic session")
	}
	call("POST", tokenPath, `{"entity_id":1,"outlet_id":1,"actions":["document_draft_review","pos_product_rename"]}`, "", cookies, 422)
	const mcp = "/api/v1/mcp/entities/1/outlets/1/document-review-proposals"
	const browser = "/api/v1/entities/1/outlets/1/document-review-proposals"
	body := `{"request_id":"44444444-0000-4000-8000-000000000001","review":{"document_id":2,"expected_revision":0,"reason":"Otra revisión de prueba"}}`
	call("POST", mcp, strings.Replace(body, `"expected_revision":0,`, "", 1), minted.Token, nil, 422)
	call("POST", mcp, strings.Replace(body, `"expected_revision":0`, `"expected_revision":null`, 1), minted.Token, nil, 422)
	created := call("POST", mcp, body, minted.Token, nil, 201)
	duplicate := call("POST", mcp, body, minted.Token, nil, 200)
	if created.Body.String() != duplicate.Body.String() || strings.Contains(created.Body.String(), "document_id") {
		t.Fatal("synthetic replay changed or disclosed document")
	}
	var receipt documentReviewWireReceipt
	if err := json.Unmarshal(created.Body.Bytes(), &receipt); err != nil || receipt.Status != "pending" {
		t.Fatal(receipt, err)
	}
	path := fmt.Sprintf("%s/%d", browser, receipt.ID)
	var view documentReviewWireDetails
	if err := json.Unmarshal(call("GET", path, "", "", cookies, 200).Body.Bytes(), &view); err != nil || view.Review == nil || view.Review.ID != 2 || view.Review.Revision != 0 || view.Review.TotalMinor != 123456 {
		t.Fatal(view, err)
	}
	otherLogin := postgresTestCookies(t, handler, "review-fixture", pw)
	call("GET", path, "", "", otherLogin, 404)
	decision := fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, view.Digest)
	call("POST", path+"/decision", decision, "", cookies, 200)
	call("POST", path+"/decision", decision, "", cookies, 409)
	var documentState string
	var reviewedRevision int64
	if err := db.QueryRowContext(ctx, `SELECT status,revision FROM documents WHERE id=2`).Scan(&documentState, &reviewedRevision); err != nil || documentState != "reviewed" || reviewedRevision != 1 {
		t.Fatal(documentState, reviewedRevision, err)
	}

}
