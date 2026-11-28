package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresAccountingWritesUseCurrentScope(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
		VALUES(1,'finance-operator','!synthetic',TRUE,FALSE,FALSE),(2,'scope-admin','!synthetic',TRUE,FALSE,FALSE);
		INSERT INTO entities(id,name,cuit,active) VALUES (1,'Fixture entity','00000000001',TRUE),(2,'Other entity','00000000002',TRUE);
		INSERT INTO outlets(id,entity_id,name,active) VALUES (1,1,'Fixture A',TRUE),(2,1,'Fixture B',TRUE),(3,2,'Other outlet',TRUE);
		INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'administration',TRUE);
		INSERT INTO outlet_grants VALUES(1,1),(1,2),(2,1),(2,2);
		INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES(1,1,'Fixture supplier','00000000003',TRUE);
		INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,
		point_of_sale,number,issue_date,currency,total_minor,description,status,revision,created_at,updated_at)
		SELECT i,1,2,1,'00000000003','invoice','A',1,i,'2026-11-01','ARS',10000,'','reviewed',1,
		'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z' FROM generate_series(1,2) i;`); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com", enableMCP: true, enableMCPReconciliationDraftProposals: true}
	router := a.routes()
	operator := nativeDocumentSession(t, a, router, 1)
	admin := nativeDocumentSession(t, a, router, 2)
	request := func(cookies []*http.Cookie, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://example.com/api/v1"+path, strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	check := func(r *httptest.ResponseRecorder, want int) {
		t.Helper()
		if r.Code != want {
			t.Fatalf("got %d, want %d: %s", r.Code, want, r.Body)
		}
	}
	decode := func(w *httptest.ResponseRecorder, target any) {
		t.Helper()
		check(w, 201)
		if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	const (
		entity     = "/entities/1"
		memberPath = entity + "/memberships/1"
		original   = `{"rol":"administration","activa":true,"outlet_ids":[1,2]}`
		downgraded = `{"rol":"read_only","activa":true,"outlet_ids":[1,2]}`
		inactive   = `{"rol":"administration","activa":false,"outlet_ids":[1,2]}`
		firstOnly  = `{"rol":"administration","activa":true,"outlet_ids":[1]}`
		payment    = `{"outlet_id":1,"supplier_id":1,"moneda":"ARS","total":"40.00","metodo":"cash"}`
		reason     = `{"motivo":"QA financial change"}`
	)
	var payable payableDetailView
	decode(request(operator, "POST", entity+"/documents/1/payable", reason), &payable)
	var draft paymentDetailView
	decode(request(operator, "POST", entity+"/payments", payment), &draft)
	var reconciliation reconciliationReadRun
	decode(request(operator, "POST", entity+"/reconciliations", `{"outlet_ids":[2]}`), &reconciliation)

	queued := func(method, path, body, change string, want int) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := lockPostgresEntity(ctx, tx, 1); err != nil {
			t.Fatal(err)
		}
		var blocker int
		if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
			t.Fatal(err)
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- request(operator, method, path, body) }()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			var waiting bool
			err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
				WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting)
			if err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			select {
			case response := <-done:
				t.Fatalf("accounting route returned before scope lock: %d %s", response.Code, response.Body)
			case <-deadline.C:
				t.Fatal("accounting request did not wait on the scope lock")
			case <-time.After(5 * time.Millisecond):
			}
		}
		manager, err := scopedMembership(tx, 2, 1, "manage_entity_access")
		if err != nil {
			t.Fatal(err)
		}
		changeRequest := httptest.NewRequest("PUT", memberPath, nil).WithContext(ctx)
		changeRequest.SetPathValue("member", "1")
		if status, _, err := saveMembership(changeRequest, tx, manager, []byte(change)); err != nil || status != 200 {
			t.Fatalf("ordinary membership update failed: %d %v", status, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case response := <-done:
			check(response, want)
		case <-deadline.C:
			t.Fatal("accounting route did not return after the scope change")
		}
		check(request(admin, "PUT", memberPath, original), 200)
	}

	paymentPath := fmt.Sprintf("%s/payments/%d", entity, draft.ID)
	apply := fmt.Sprintf(`{"idempotency_key":%q,"aplicaciones":[{"payable_id":%d,"importe":"40.00"}]}`, draft.Key, payable.ID)
	queued("POST", entity+"/documents/2/payable", reason, inactive, 404)
	queued("POST", entity+"/payments", payment, downgraded, 403)
	queued("POST", paymentPath+"/post", apply, firstOnly, 404)
	check(request(operator, "POST", paymentPath+"/post", apply), 200)
	queued("POST", paymentPath+"/reverse", reason, downgraded, 403)
	queued("POST", entity+"/reconciliations", `{"outlet_ids":[2]}`, downgraded, 403)
	deletePath := fmt.Sprintf("%s/reconciliations/%d?expected_revision=1&operation_key=scope-check", entity, reconciliation.ID)
	queued("DELETE", deletePath, "", firstOnly, 404)
	check(request(operator, "POST", "/entities/2/payments", payment), 404)

	var proposalToken struct{ Token string }
	decode(request(operator, "POST", strings.TrimPrefix(proposalSessionsAuthPath, "/api/v1"),
		`{"entity_id":1,"outlet_id":2,"actions":["reconciliation_draft_create"]}`), &proposalToken)
	propose := func(path string) *httptest.ResponseRecorder {
		const suggestion = `{"request_id":"44444444-0000-4000-8000-000000000003","draft":{"match_reference":false}}`
		r := httptest.NewRequestWithContext(ctx, "POST", "http://example.com/api/v1/mcp"+path, strings.NewReader(suggestion))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+proposalToken.Token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	const proposals = entity + "/outlets/2/reconciliation-draft-proposals"
	check(propose("/entities/1/outlets/1/reconciliation-draft-proposals"), 404)
	check(propose("/entities/2/outlets/3/reconciliation-draft-proposals"), 404)
	var proposal reconciliationDraftWireReceipt
	decode(propose(proposals), &proposal)
	detailPath := fmt.Sprintf("%s/%d", proposals, proposal.ID)
	var detail reconciliationDraftWireDetails
	response := request(operator, "GET", detailPath, "")
	check(response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || len(detail.Digest) != 64 {
		t.Fatalf("missing scoped proposal preview: %v", err)
	}
	decision := fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, detail.Digest)
	queued("POST", detailPath+"/decision", decision, downgraded, 403)
	queued("POST", detailPath+"/decision", decision, firstOnly, 404)

	var payables, payments, runs int
	var paymentStatus, runState string
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM ap_payables),(SELECT count(*) FROM ap_payments),
		(SELECT count(*) FROM reconciliation_read_runs),(SELECT status FROM ap_payments WHERE id=$1),
		(SELECT state FROM reconciliation_read_runs WHERE id=$2)`, draft.ID, reconciliation.ID).
		Scan(&payables, &payments, &runs, &paymentStatus, &runState); err != nil {
		t.Fatal(err)
	}
	if payables != 1 || payments != 1 || runs != 1 || paymentStatus != "posted" || runState != "draft" {
		t.Fatalf("a denied operation changed accounting state: %d %d %d %s %s", payables, payments, runs, paymentStatus, runState)
	}
}
