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

func TestReservationPolicyNoticeFixedWording(t *testing.T) {
	for _, test := range []struct {
		hours int
		unit  string
	}{{1, "hora"}, {24, "horas"}, {72, "horas"}} {
		got, err := formatReservationPolicyNotice(test.hours)
		if err != nil || got != fmt.Sprintf("Por favor, avisá al local con al menos %d %s de anticipación si no podés asistir.", test.hours, test.unit) {
			t.Fatal(got, err)
		}
	}
	for _, raw := range []string{`{}`, `{"notice_hours":null}`, `{"notice_hours":0}`, `{"notice_hours":73}`, `{"notice_hours":1.5}`, `{"notice_hours":24,"text":"Extra"}`, `{"notice_hours":1,"notice_hours":24}`} {
		if _, err := decodeReservationPolicyNotice([]byte(raw)); err == nil {
			t.Fatal("accepted untyped policy", raw)
		}
	}
}

func TestPostgresMCPReservationPolicyNoticeWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-policy-only"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'policy-admin',$1,TRUE,FALSE,FALSE),(2,'policy-accounting',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	const old = "Versión histórica de prueba."
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE),(2,'Other entity','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'Fixture branch',TRUE),(2,1,'Sibling branch',TRUE),(3,2,'Other branch',TRUE),(4,1,'Inactive branch',FALSE),(5,1,'No venue',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE),(2,2,1,'accounting',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,4),(1,5),(2,1);
 INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled,collect_contact) VALUES
 (1,'policy-fixture','Fixture venue','Versión histórica de prueba.',TRUE,TRUE),
 (2,'policy-sibling','Sibling venue','Sibling wording',FALSE,FALSE),(4,'policy-inactive','Inactive venue','Inactive wording',FALSE,FALSE);
 INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(1,'Mesa 1',4),(2,'Mesa 1',4);
 INSERT INTO reservation_menus(outlet_id,body,updated_at,updated_by) VALUES (1,'synthetic-menu-metadata', '2026-11-22T12:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	day := now.In(reservationLocation).AddDate(0, 0, 1)
	start := time.Date(day.Year(), day.Month(), day.Day(), 21, 0, 0, 0, reservationLocation).UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx, `INSERT INTO reservation_slots(outlet_id,start_at) VALUES(1,$1),(2,$1)`, start); err != nil {
		t.Fatal(err)
	}
	a := app{db: db, host: "example.com", enableMCP: true, publicReservations: true, enableMCPReservationPolicyNoticeProposals: true, mcpClock: func() time.Time { return now }}
	h := a.routes()
	admin := postgresTestCookies(t, h, "policy-admin", password)
	secondLogin := postgresTestCookies(t, h, "policy-admin", password)
	accounting := postgresTestCookies(t, h, "policy-accounting", password)
	invoke := func(method, path, body, token string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if cookies != nil {
			r.Header.Set("Origin", "http://example.com")
			for _, c := range cookies {
				r.AddCookie(c)
				if c.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", c.Value)
				}
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	call := func(method, path, body, token string, cookies []*http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := invoke(method, path, body, token, cookies)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	const public = "/api/v1/public/reservas/policy-fixture"
	const native = "/api/v1/entities/1/outlets/1/reservations"
	const api = "/api/v1/mcp/entities/1/outlets/1/reservation-policy-notice-proposals"
	const browser = "/api/v1/entities/1/outlets/1/reservation-policy-notice-proposals"
	config := func(policy string, enabled bool) string {
		return fmt.Sprintf(`{"venue":"policy-fixture","display_name":"Fixture venue","cancellation_policy":%q,"enabled":%t}`, policy, enabled)
	}
	newBooking := func(name, policy string) reservationResult {
		t.Helper()
		w := call("POST", public+"/requests", fmt.Sprintf(`{"guest_name":%q,"start_at":%q,"party_size":2}`, name, start), "", nil, 201)
		var result reservationResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.CancellationPolicy == nil || *result.CancellationPolicy != policy {
			t.Fatal(result, err)
		}
		return result
	}
	// Actual local native booking captures the old current policy, not a test-side snapshot insert.
	oldBooking := newBooking("Synthetic old guest", old)
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["reservation_cancellation_policy_change"]}`
	disabledApp := &app{db: db, host: "example.com", enableMCP: true}
	nr := httptest.NewRecorder()
	disabledApp.routes().ServeHTTP(nr, httptest.NewRequest("POST", "http://example.com"+api, nil))
	if nr.Code != 404 {
		t.Fatal("feature routes active by default")
	}
	call("POST", proposalSessionsAuthPath, issue, "", accounting, 403)
	call("POST", proposalSessionsAuthPath, strings.Replace(issue, `"outlet_id":1`, `"outlet_id":4`, 1), "", admin, 404)
	key := func(input string, cookies []*http.Cookie) string {
		t.Helper()
		var result struct{ Token string }
		if err := json.Unmarshal(call("POST", proposalSessionsAuthPath, input, "", cookies, 201).Body.Bytes(), &result); err != nil || len(result.Token) != 43 {
			t.Fatal("missing synthetic key", err)
		}
		return result.Token
	}
	token := key(issue, admin)
	altToken := key(issue, secondLogin)
	body := func(id, hours int) string {
		return fmt.Sprintf(`{"request_id":"55555555-0000-4000-8000-%012d","policy_change":{"notice_hours":%d}}`, id, hours)
	}
	call("POST", api, body(1, 24), token, nil, 409) // active venue
	call("PUT", native+"/settings", config(old, false), "", admin, 200)
	absent := key(strings.Replace(issue, `"outlet_id":1`, `"outlet_id":5`, 1), admin)
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/5/", 1), body(1, 24), absent, nil, 404)
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/2/", 1), body(1, 24), token, nil, 404)
	call("POST", strings.Replace(api, "/entities/1/", "/entities/2/", 1), body(1, 24), token, nil, 404)
	call("POST", api, body(1, 24), token, admin, 422)
	for _, bad := range []int{0, 73, -1} {
		call("POST", api, body(1, bad), token, nil, 422)
	}
	call("GET", public, "", "", nil, 404)

	facts := func() string {
		t.Helper()
		var got string
		err := db.QueryRowContext(ctx, `SELECT md5(concat(
		 (SELECT json_agg(to_jsonb(v)-'cancellation_policy'-'enabled' ORDER BY outlet_id)::text FROM reservation_venues v),
		 (SELECT row_to_json(v)::text FROM reservation_venues v WHERE outlet_id=2),
		 (SELECT json_agg(x ORDER BY outlet_id,label)::text FROM reservation_tables x),
		 (SELECT json_agg(x ORDER BY outlet_id,start_at)::text FROM reservation_slots x),
		 (SELECT json_agg(x ORDER BY id)::text FROM reservation_requests x),
		 (SELECT json_agg(x ORDER BY request_id)::text FROM reservation_request_policies x),
		 (SELECT json_agg(x ORDER BY outlet_id)::text FROM reservation_menus x),
		 (SELECT count(*) FROM reservation_customers),(SELECT count(*) FROM reservation_private_contacts),
		 (SELECT count(*) FROM reservation_concierge_events)
		))`).Scan(&got)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := facts()
	check := func(policy string, enabled bool) {
		t.Helper()
		var text string
		var on bool
		if err := db.QueryRowContext(ctx, `SELECT cancellation_policy,enabled FROM reservation_venues WHERE outlet_id=1`).Scan(&text, &on); err != nil || text != policy || on != enabled {
			t.Fatal("unexpected policy or activation", text, on, err)
		}
		if facts() != before {
			t.Fatal("changed existing native booking or protected settings")
		}
	}
	type queued struct{ path, digest string }
	queue := func(id, hours int, current string) queued {
		t.Helper()
		first := call("POST", api, body(id, hours), token, nil, 201)
		retry := call("POST", api, body(id, hours), token, nil, 200)
		var fields map[string]json.RawMessage
		if first.Body.String() != retry.Body.String() || json.Unmarshal(first.Body.Bytes(), &fields) != nil || len(fields) != 3 || fields["id"] == nil || fields["status"] == nil || fields["expires_at"] == nil {
			t.Fatal("non-neutral bearer receipt")
		}
		var receipt reservationPolicyNoticeWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.Status != "pending" || receipt.ID <= 0 {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", secondLogin, 404)
		var entry reservationPolicyNoticeWireDetails
		got := call("GET", path, "", "", admin, 200)
		proposed, _ := formatReservationPolicyNotice(hours)
		if err := json.Unmarshal(got.Body.Bytes(), &entry); err != nil || entry.PolicyChange == nil || entry.PolicyChange.NoticeHours != hours || entry.PolicyChange.Current != current || entry.PolicyChange.Proposed != proposed || entry.PolicyChange.VenueEnabled || len(entry.Digest) != 64 {
			t.Fatal(got.Body, err)
		}
		for _, v := range []string{"Synthetic old guest", "policy-fixture", "collect_contact", "guest_name", "table_label"} {
			if strings.Contains(got.Body.String(), v) {
				t.Fatal("unrelated field", v)
			}
		}
		return queued{path, entry.Digest}
	}
	decide := func(action, digest string, ack bool) string {
		extra := ""
		if ack {
			extra = `,"acknowledge_future_requests_only":true`
		}
		return fmt.Sprintf(`{"decision":%q,"digest":%q,"confirm":true%s}`, action, digest, extra)
	}
	selected := queue(1, 24, old)
	call("POST", api, body(1, 24), altToken, nil, 409)
	call("POST", selected.path+"/decision", decide("approve", selected.digest, true), "", secondLogin, 404)
	call("POST", selected.path+"/decision", decide("approve", selected.digest, false), "", admin, 422)
	check(old, false)
	call("POST", selected.path+"/decision", decide("approve", selected.digest, true), "", admin, 200)
	call("POST", selected.path+"/decision", decide("approve", selected.digest, true), "", admin, 409)
	receipt := call("POST", api, body(1, 24), token, nil, 200).Body.String()
	if !strings.Contains(receipt, "approved") || strings.Contains(receipt, "policy") {
		t.Fatal("nonredacted terminal receipt")
	}
	policy, _ := formatReservationPolicyNotice(24)
	check(policy, false)
	call("GET", public, "", "", nil, 404)

	noChange := queue(2, 24, policy) // same bearer response shape and status as a real change
	call("POST", noChange.path+"/decision", decide("approve", noChange.digest, true), "", admin, 422)
	call("POST", noChange.path+"/decision", decide("reject", noChange.digest, false), "", admin, 200)
	rejected := queue(3, 48, policy)
	call("POST", rejected.path+"/decision", decide("reject", rejected.digest, false), "", admin, 200)
	check(policy, false)
	prev := queue(4, 12, policy)
	call("PUT", native+"/settings", config("Texto concurrente.", false), "", admin, 200)
	call("POST", prev.path+"/decision", decide("approve", prev.digest, true), "", admin, 409)
	check("Texto concurrente.", false)
	call("PUT", native+"/settings", config(policy, false), "", admin, 200)

	race := queue(5, 36, policy)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var lockPID int
	if err = tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&lockPID); err != nil {
		t.Fatal(err)
	}
	// Same native mutation lock and save function as PUT /reservations/settings.
	if err = lockPostgresOutlet(ctx, tx, 1); err != nil {
		t.Fatal(err)
	}
	hreq := httptest.NewRequestWithContext(ctx, "PUT", "http://example.com"+native+"/settings", nil)
	if _, _, err = reservationSettingsSave(hreq, tx, 1, []byte(config(policy, true))); err != nil {
		t.Fatal(err)
	}
	waiter := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		waiter <- invoke("POST", race.path+"/decision", decide("approve", race.digest, true), "", admin)
	}()
	for deadline := time.Now().Add(3 * time.Second); ; {
		var blocked bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, lockPID).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval did not block on the native outlet lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if w := <-waiter; w.Code != 409 {
		t.Fatal("concurrent enable was not respected", w.Code, w.Body)
	}
	check(policy, true)
	call("GET", public, "", "", nil, 200)
	newItem := newBooking("Synthetic new guest", policy)
	var oldText, newText string
	if err = db.QueryRowContext(ctx, `SELECT (SELECT policy FROM reservation_request_policies WHERE request_id=$1),
 (SELECT policy FROM reservation_request_policies WHERE request_id=$2)`, oldBooking.ID, newItem.ID).Scan(&oldText, &newText); err != nil || oldText != old || newText != policy {
		t.Fatal("changed booking snapshot", err)
	}
	nativeRows := call("GET", native+"?date="+day.Format(time.DateOnly), "", "", admin, 200).Body.String()
	if !strings.Contains(nativeRows, old) || !strings.Contains(nativeRows, policy) {
		t.Fatal("native staff queue lost a captured policy")
	}

	call("PUT", native+"/settings", config(policy, false), "", admin, 200)
	expiring := queue(6, 8, policy)
	now = now.Add(16 * time.Minute)
	call("POST", expiring.path+"/decision", decide("approve", expiring.digest, true), "", admin, 410)
	call("POST", api, body(6, 8), token, nil, 401)
	fresh := key(issue, admin)
	var remaining reservationPolicyNoticeWireReceipt
	if err = json.Unmarshal(call("POST", api, body(7, 8), fresh, nil, 201).Body.Bytes(), &remaining); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1`); err != nil {
		t.Fatal(err)
	}
	call("GET", fmt.Sprintf("%s/%d", browser, remaining.ID), "", "", admin, 404)
	call("POST", api, body(7, 8), fresh, nil, 404)
}
