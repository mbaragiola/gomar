package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresReconciliationLifecycle(t *testing.T) {
	ctx, db := testPostgresDB(t)
	var hasSchema bool
	if err := db.QueryRowContext(ctx, "SELECT to_regnamespace('gomar') IS NOT NULL").Scan(&hasSchema); err != nil || hasSchema {
		t.Fatalf("requires empty QA database: schema present=%t: %v", hasSchema, err)
	}
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const fixture = `
	INSERT INTO users(id,username,password,active,staff,superuser) VALUES (1,'fixture-reviewer','!synthetic',TRUE,FALSE,FALSE);
	INSERT INTO entities(id,name,cuit,active) VALUES (1,'Fixture Entity','00000000000',TRUE);
	INSERT INTO outlets(id,entity_id,name,active) VALUES (1,1,'Fixture Outlet',TRUE);
	INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES (1,1,1,'administration',TRUE);
	INSERT INTO outlet_grants(membership_id,outlet_id) VALUES (1,1);`
	if _, err := db.ExecContext(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	const password = "generated-fixture-access"
	if _, err := db.ExecContext(ctx, "UPDATE users SET password=$1 WHERE id=1", encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	a := &app{db: db, host: "example.com"}
	handler := a.routes()
	invoke := func(r *http.Request, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r = r.WithContext(ctx)
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", c.Value)
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	csrf := invoke(httptest.NewRequest("GET", "/api/v1/auth/csrf", nil), nil)
	login := httptest.NewRequest("POST", "/api/v1/auth/login",
		strings.NewReader(fmt.Sprintf(`{"usuario":"fixture-reviewer","clave":%q}`, password)))
	login.Header.Set("Content-Type", "application/json")
	session := invoke(login, csrf.Result().Cookies())
	if csrf.Code != 200 || session.Code != 200 {
		t.Fatalf("fixture login failed: csrf=%d login=%d", csrf.Code, session.Code)
	}
	cookies := session.Result().Cookies()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/entities/1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return invoke(r, cookies)
	}
	check := func(res *httptest.ResponseRecorder, status int) *httptest.ResponseRecorder {
		t.Helper()
		if res.Code != status {
			t.Fatalf("status=%d want=%d body=%s", res.Code, status, res.Body)
		}
		return res
	}
	check(request("POST", "/reconciliations", `{"outlet_ids":[1],"match_reference":true}`), 201)
	const run = "/reconciliations/1"
	const csv = "fecha,importe,referencia\n2026-11-01,10.00,fixture001\n"
	const mapping = `{"date":0,"amount":1,"reference":2}`
	for i, side := range []string{"payments", "cash_flow"} {
		var body bytes.Buffer
		upload := multipart.NewWriter(&body)
		for key, value := range map[string]string{
			"mapping": mapping, "date_format": "%Y-%m-%d", "default_currency": "ARS",
			"default_outlet_id": "1", "side": side, "expected_revision": fmt.Sprint(i + 1), "operation_key": "fixture-" + side,
		} {
			if err := upload.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
		file, err := upload.CreateFormFile("file", "synthetic.csv")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(csv)); err != nil {
			t.Fatal(err)
		}
		if err := upload.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/v1/entities/1"+run+"/sources", &body)
		r.Header.Set("Content-Type", upload.FormDataContentType())
		check(invoke(r, cookies), 201)
	}
	var rows reconciliationDetailPage
	decodeReconciliationFixture(t, check(request("GET", run+"/rows?side=payments&page_size=1", ""), 200).Body.Bytes(), &rows)
	if rows.Total != 1 || len(rows.Items) != 1 {
		t.Fatalf("unexpected row page: %+v", rows)
	}

	start := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			done <- request("POST", run+"/prepare", `{"expected_revision":3,"operation_key":"prepare"}`)
		}()
	}
	close(start)
	for range 2 {
		check(<-done, 200)
	}
	var candidates struct {
		Items []reconciliationCandidateRead `json:"items"`
		Total int64                         `json:"total"`
	}
	decodeReconciliationFixture(t, check(request("GET", run+"/candidates?member_page_size=1", ""), 200).Body.Bytes(), &candidates)
	if candidates.Total != 1 || len(candidates.Items) != 1 || candidates.Items[0].PaymentCount != 1 || candidates.Items[0].CashFlowCount != 1 {
		t.Fatalf("unexpected preparation: %+v", candidates)
	}
	check(request("POST", run+"/decisions", `{"expected_revision":4,"operation_key":"confirm","decision":"confirmed","payment_row_id":1,"cash_flow_row_id":2}`), 200)
	// The owned synthetic trigger refuses the ordinary final audit write.
	// Both the prior persisted revision and full reviewed run must survive.
	const failure = `
	CREATE FUNCTION gomar.test_reconciliation_audit_failure() RETURNS trigger
	LANGUAGE plpgsql SET search_path = pg_catalog AS $f$
	BEGIN
		IF CAST(NEW.payload AS json)->>'action'='close' THEN RAISE EXCEPTION 'synthetic audit unavailable'; END IF;
		RETURN NEW;
	END;
	$f$;
	CREATE TRIGGER test_reconciliation_audit_failure BEFORE INSERT ON reconciliation_detail_events
		FOR EACH ROW EXECUTE FUNCTION gomar.test_reconciliation_audit_failure();`
	if _, err := db.ExecContext(ctx, failure); err != nil {
		t.Fatal(err)
	}
	check(request("POST", run+"/close", `{"expected_revision":5,"operation_key":"close"}`), 500)
	var state string
	var revision, count int
	stateAt := func(want string, version, events int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT state,revision,
			(SELECT count(*) FROM reconciliation_detail_events WHERE run_id=r.id)
			FROM reconciliation_read_runs r WHERE id=$1`, 1).Scan(&state, &revision, &count); err != nil {
			t.Fatal(err)
		}
		if state != want || revision != version || count != events {
			t.Fatalf("state=%s revision=%d events=%d, want %s/%d/%d", state, revision, count, want, version, events)
		}
	}
	stateAt("review", 5, 5)
	if _, err := db.ExecContext(ctx, "DROP TRIGGER test_reconciliation_audit_failure ON reconciliation_detail_events"); err != nil {
		t.Fatal(err)
	}
	check(request("POST", run+"/close", `{"expected_revision":5,"operation_key":"close"}`), 200)
	stateAt("closed", 6, 6)
	var log reconciliationDetailPage
	decodeReconciliationFixture(t, check(request("GET", run+"/events?page_size=2&page=3", ""), 200).Body.Bytes(), &log)
	if log.Total != 6 || len(log.Items) != 2 || !containsAuditAction(log, "close") {
		t.Fatalf("unexpected closing audit: %+v", log)
	}
}

func containsAuditAction(page reconciliationDetailPage, action string) bool {
	for _, item := range page.Items {
		var event struct {
			Action string `json:"action"`
			Actor  string `json:"actor"`
		}
		if err := json.Unmarshal(item, &event); err == nil && event.Action == action && event.Actor == "fixture-reviewer" {
			return true
		}
	}
	return false
}

func decodeReconciliationFixture(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("could not decode synthetic reconciliation response: %v", err)
	}
}
