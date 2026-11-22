package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresPayableCrossOutletSettlement(t *testing.T) {
	ctx, db := testPostgresDB(t)
	var hasSchema bool
	if err := db.QueryRowContext(ctx, `SELECT to_regnamespace('gomar') IS NOT NULL`).Scan(&hasSchema); err != nil || hasSchema {
		t.Fatalf("requires empty QA database: schema present=%t: %v", hasSchema, err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-accounting-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
       VALUES (1,'fixture-accountant',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	const fixture = `
    INSERT INTO entities(id,name,cuit,active) VALUES (1,'Fixture Entity','00000000000',TRUE);
    INSERT INTO outlets(id,entity_id,name,active) VALUES (1,1,'Fixture A',TRUE),(2,1,'Fixture B',TRUE);
    INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES (1,1,1,'accounting',TRUE);
    INSERT INTO outlet_grants VALUES (1,1),(1,2);
    INSERT INTO suppliers(id,entity_id,name,cuit,active) VALUES (1,1,'Synthetic Supplier','00000000001',TRUE);
    INSERT INTO documents(id,entity_id,outlet_id,supplier_id,issuer_cuit,document_type,letter,
      point_of_sale,number,issue_date,due_date,currency,total_minor,description,status,revision,created_at,updated_at)
    VALUES (1,1,2,1,'00000000001','invoice','A',1,1,'2026-11-01','2026-11-11','ARS',10000,'','reviewed',1,
      '2026-11-01T00:00:00Z','2026-11-01T00:00:00Z');`
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com", payablesClock: func() time.Time { return time.Date(2026, 11, 20, 15, 30, 2, 654321000, time.UTC) }}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	a.privateRoot = root
	handler := a.routes()
	var cookies []*http.Cookie
	invoke := func(r *http.Request) *httptest.ResponseRecorder {
		r = r.WithContext(ctx)
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, r)
		return result
	}
	bootstrap := invoke(httptest.NewRequest("GET", "/api/v1/auth/csrf", nil))
	if bootstrap.Code != 200 {
		t.Fatalf("CSRF bootstrap status=%d", bootstrap.Code)
	}
	cookies = bootstrap.Result().Cookies()
	login := invoke(httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"usuario":"fixture-accountant","clave":`+fmt.Sprintf("%q", password)+`}`)))
	if login.Code != 200 {
		t.Fatalf("synthetic login status=%d: %s", login.Code, login.Body)
	}
	cookies = login.Result().Cookies()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/entities/1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return invoke(r)
	}
	check := func(response *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
		t.Helper()
		if response.Code != status {
			t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body)
		}
		return response
	}
	call := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		return check(request(method, path, body), status)
	}
	decode := func(r *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(r.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	var payable payableDetailView
	decode(call("POST", "/documents/1/payable", `{"motivo":"Alta sintética"}`, 201), &payable)
	if payable.Balance != "100.00" || payable.State != "open" {
		t.Fatalf("unexpected initial balance: %+v", payable)
	}
	call("POST", "/documents/1/payable", `{"motivo":"Alta duplicada"}`, 409)
	call("GET", "/payables?due_from=2026-11-01&due_to=2026-11-15&outlet_id=2&supplier_id=1&currency=ARS&state=open", "", 200)
	const bodyTemplate = `{"outlet_id":%d,"supplier_id":1,"moneda":"ARS","total":"60.00","metodo":"cash"}`
	drafts := make([]paymentDetailView, 2)
	for i := range drafts {
		decode(call("POST", "/payments", fmt.Sprintf(bodyTemplate, i+1), 201), &drafts[i])
		if drafts[i].ID < 1 || drafts[i].Status != "draft" || drafts[i].Key == "" {
			t.Fatal("draft missing identity")
		}
	}
	path := func(i int) string { return fmt.Sprintf("/payments/%d", drafts[i].ID) }
	paymentApply := func(i int) string {
		return fmt.Sprintf(`{"idempotency_key":%q,"aplicaciones":[{"payable_id":%d,"importe":"60.00"}]}`, drafts[i].Key, payable.ID)
	}
	// Ensures two different payment outlets compete for one invoice in the
	// same entity. The first consumes 60; the second must observe only 40.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockPostgresEntity(ctx, tx, 1); err != nil {
		t.Fatal(err)
	}
	type result struct {
		index    int
		response *httptest.ResponseRecorder
	}
	results := make(chan result, 2)
	for i := range drafts {
		go func() { results <- result{i, request("POST", path(i)+"/post", paymentApply(i))} }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().InUse < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if db.Stats().InUse < 3 {
		t.Fatal("did not observe both pooled AP request transactions")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	winner, loser := -1, -1
	var winnerReceipt string
	for range 2 {
		select {
		case result := <-results:
			switch result.response.Code {
			case http.StatusOK:
				winner = result.index
				winnerReceipt = result.response.Body.String()
			case http.StatusUnprocessableEntity:
				if !strings.Contains(result.response.Body.String(), "El importe aplicado supera el saldo") {
					t.Fatalf("unexpected payment refusal: %s", result.response.Body)
				}
				loser = result.index
			default:
				t.Fatalf("unexpected payment status=%d: %s", result.response.Code, result.response.Body)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if winner < 0 || loser < 0 || winner == loser {
		t.Fatal("expected one settlement and one refusal")
	}
	if replay := call("POST", path(winner)+"/post", paymentApply(winner), 200).Body.String(); replay != winnerReceipt {
		t.Fatal("exact retry changed the receipt")
	}
	var partial payableDetailView
	payableURL := fmt.Sprintf("/payables/%d", payable.ID)
	decode(call("GET", payableURL, "", 200), &partial)
	if partial.Balance != "40.00" || partial.State != "partial" {
		t.Fatalf("unexpected balance: %+v", partial)
	}
	totals, err := a.summarizeOutstandingPayables(ctx, user{id: 1}, 1, 2)
	if err != nil || len(totals.Totals) != 1 || totals.Totals[0].OutstandingMinor != 4000 || totals.Totals[0].OverdueMinor != 4000 {
		t.Fatalf("unexpected summary: %+v (%v)", totals, err)
	}
	call("GET", "/payments", "", 200)
	const pngType = "image/png"
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	receiptURL := path(winner) + "/receipt"
	upload := func() *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		part, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": {`form-data; name="archivo"; filename="synthetic.png"`},
			"Content-Type":        {pngType},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(picture.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/v1/entities/1"+receiptURL, &body)
		r.Header.Set("Content-Type", w.FormDataContentType())
		return invoke(r)
	}
	check(upload(), 201)
	check(upload(), 409)
	downloaded := call("GET", receiptURL, "", 200)
	if !bytes.Equal(downloaded.Body.Bytes(), picture.Bytes()) || downloaded.Header().Get("Content-Type") != pngType {
		t.Fatal("backing private receipt differs from synthetic upload")
	}
	call("POST", path(winner)+"/reverse", `{"motivo":"Reversión sintética"}`, 201)
	var restored payableDetailView
	decode(call("GET", payableURL, "", 200), &restored)
	if restored.Balance != "100.00" || restored.State != "open" {
		t.Fatalf("unexpected restored balance: %+v", restored)
	}
	var allocations, ledger, events, receipts, stillDraft int
	err = db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM ap_allocations),(SELECT count(*) FROM ap_ledger),
        (SELECT count(*) FROM ap_financial_events),(SELECT count(*) FROM ap_receipts),(SELECT count(*) FROM ap_payments WHERE status='draft')`).
		Scan(&allocations, &ledger, &events, &receipts, &stillDraft)
	if err != nil || allocations != 2 || ledger != 3 || events != 6 || receipts != 1 || stillDraft != 1 {
		t.Fatalf("wrong history after rollback/retry/reversal: allocations=%d ledger=%d events=%d receipts=%d drafts=%d err=%v", allocations, ledger, events, receipts, stillDraft, err)
	}
}
