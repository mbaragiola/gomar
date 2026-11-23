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

func TestPostgresMCPReservationSlotAddWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-reservation-slot-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'reservation-slot-workflow',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	future := time.Now().In(reservationLocation).AddDate(0, 0, 2)
	future = time.Date(future.Year(), future.Month(), future.Day(), 22, 30, 0, 0, reservationLocation)
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE),(2,'Another fixture','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE),(3,2,'C',TRUE),(4,1,'D',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,4);
 INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled,collect_contact) VALUES
 (1,'synthetic-table','Fixture venue','Private policy sentinel',FALSE,TRUE),
 (2,'synthetic-other','Other venue','Second policy',FALSE,FALSE);
 INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(1,'Mesa 1',4),(2,'Mesa 2',4);`); err != nil {
		t.Fatal(err)
	}
	when := future.Add(-90 * time.Minute).UTC().Format(time.RFC3339)
	for _, sql := range []string{
		`INSERT INTO reservation_slots(outlet_id,start_at) VALUES(1,$1),(2,$1)`,
		`INSERT INTO reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,created_at)
      VALUES('01010101010101010101010101010101',1,$1,'Synthetic hidden guest',2,'pending','2026-11-01T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, sql, when); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reservation_customers(id,entity_id,phone_e164)
 VALUES('11111111-0000-4000-8000-000000000001',1,'+5491100000000');
 INSERT INTO reservation_private_contacts(reservation_id,outlet_id,entity_id,customer_id,note)
 VALUES('01010101010101010101010101010101',1,1,'11111111-0000-4000-8000-000000000001','Synthetic private note')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := app{db: db, mcpClock: func() time.Time { return now }, host: "example.com", enableMCP: true, publicReservations: true, reservationContacts: true, enableMCPReservationSlotAddProposals: true}
	h := a.routes()
	cookies := postgresTestCookies(t, h, "reservation-slot-workflow", password)
	otherLogin := postgresTestCookies(t, h, "reservation-slot-workflow", password)
	call := func(method, path, body, token string, login []*http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, "http://example.com"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
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
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}

	const api = "/api/v1/mcp/entities/1/outlets/1/reservation-slot-add-proposals"
	const browser = "/api/v1/entities/1/outlets/1/reservation-slot-add-proposals"
	const settings = "/api/v1/entities/1/outlets/1/reservations/settings"
	const native = "/api/v1/entities/1/outlets/1/reservations/slots"
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["reservation_slot_add"]}`
	const nativeEnable = `{"venue":"synthetic-table","display_name":"Fixture venue","cancellation_policy":"Private policy sentinel","enabled":true}`
	off := app{db: db, host: "example.com", enableMCP: true, publicReservations: true}
	noRoute := httptest.NewRecorder()
	off.routes().ServeHTTP(noRoute, httptest.NewRequest("POST", "http://example.com"+api, nil))
	if noRoute.Code != 404 {
		t.Fatal("unexpected feature-on route")
	}
	key := func(path, input string, login []*http.Cookie) string {
		t.Helper()
		var data struct{ Token string }
		if err := json.Unmarshal(call("POST", path, input, "", login, 201).Body.Bytes(), &data); err != nil || len(data.Token) != 43 {
			t.Fatal("cannot issue synthetic key")
		}
		return data.Token
	}
	token := key(proposalSessionsAuthPath, issue, cookies)
	separate := key(proposalSessionsAuthPath, issue, otherLogin)
	read := key("/api/v1/auth/mcp-token", `{"entity_id":1,"outlet_id":1}`, cookies)
	body := func(id int, raw string) string {
		return fmt.Sprintf(`{"request_id":"55555555-0000-4000-8000-%012d","slot_add":{"start_at":%q}}`, id, raw)
	}
	utc := func(at time.Time) string { return at.UTC().Format(time.RFC3339) }
	start := utc(future)
	call("POST", api, body(1, start), read, nil, 401)
	call("POST", api, body(1, start), token, cookies, 422)
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/2/", 1), body(1, start), token, nil, 404)
	call("POST", strings.Replace(api, "/entities/1/", "/entities/2/", 1), body(1, start), token, nil, 404)
	for _, invalid := range []string{
		future.Format(time.RFC3339), "2030-01-01T19:00:00", "2030-01-01T19:00:01Z",
		strings.Replace(start, ":00Z", ":00.0Z", 1), utc(future.Add(time.Minute)),
		utc(future.AddDate(0, 0, 60)), utc(now.AddDate(0, 0, -1)), "",
	} {
		call("POST", api, body(1, invalid), token, nil, 422)
	}
	call("POST", api, body(1, when), token, nil, 409)
	unconfigured := key(proposalSessionsAuthPath, strings.Replace(issue, `"outlet_id":1`, `"outlet_id":4`, 1), cookies)
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/4/", 1), body(1, start), unconfigured, nil, 404)
	fingerprint := func(includeSettings bool) string {
		t.Helper()
		var digest string
		err := db.QueryRowContext(ctx, `SELECT md5(concat(
  (SELECT json_agg(x ORDER BY outlet_id)::text FROM reservation_venues x WHERE $1),
  (SELECT json_agg(x ORDER BY outlet_id,label)::text FROM reservation_tables x),
  (SELECT json_agg(x ORDER BY id)::text FROM reservation_requests x),
  (SELECT json_agg(x ORDER BY request_id)::text FROM reservation_request_policies x),
  (SELECT json_agg(x ORDER BY id)::text FROM reservation_customers x),
  (SELECT json_agg(x ORDER BY reservation_id)::text FROM reservation_private_contacts x),
  (SELECT json_agg(x ORDER BY outlet_id,start_at)::text FROM reservation_slots x WHERE outlet_id<>1)
 ))`, includeSettings).Scan(&digest)
		if err != nil || digest == "" {
			t.Fatal("cannot snapshot native reservation facts")
		}
		return digest
	}
	slot := func(outlet int, starts string, want bool) {
		t.Helper()
		var found bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_slots WHERE outlet_id=$1 AND start_at=$2)`, outlet, starts).Scan(&found)
		if err != nil || found != want {
			t.Fatal("unexpected native slot state", outlet, starts, found, err)
		}
	}
	type queued struct{ path, decision, digest, expires string }
	queue := func(id int, at time.Time) queued {
		t.Helper()
		raw := body(id, utc(at))
		first := call("POST", api, raw, token, nil, 201)
		if retry := call("POST", api, raw, token, nil, 200).Body.String(); retry != first.Body.String() || strings.Contains(retry, "start_at") {
			t.Fatal("unbounded or inconsistent pending proposal receipt")
		}
		var receipt reservationSlotAddWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID <= 0 || receipt.Status != "pending" {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		var detail reservationSlotAddWireDetails
		wire := call("GET", path, "", "", cookies, 200).Body.String()
		for _, secret := range []string{"policy", "synthetic-table", "guest", "note", "+549", "table_label", "reservation_reference"} {
			if strings.Contains(wire, secret) {
				t.Fatal("private field appeared in preview:", secret)
			}
		}
		if err := json.Unmarshal([]byte(wire), &detail); err != nil || detail.SlotAdd == nil ||
			detail.SlotAdd.StartAt != utc(at) || detail.SlotAdd.StartLocal != at.In(reservationLocation).Format(time.RFC3339) ||
			detail.SlotAdd.Timezone != reservationTimezone || detail.SlotAdd.VenueEnabled || len(detail.Digest) != 64 {
			t.Fatal(detail, err)
		}
		return queued{path, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true,"acknowledge_no_remove":true}`, detail.Digest), detail.Digest, receipt.Expires}
	}
	// The same UTC instant independently exists at a different native outlet.
	if _, err := db.ExecContext(ctx, "INSERT INTO reservation_slots(outlet_id,start_at) VALUES(2,$1)", start); err != nil {
		t.Fatal(err)
	}
	before, noSettings := fingerprint(true), fingerprint(false)
	first, duplicate := queue(10, future), queue(11, future)
	if start[:10] == future.Format(time.DateOnly) {
		t.Fatal("fixture must cross the UTC date")
	}
	call("POST", api, body(10, start), separate, nil, 409)
	call("POST", first.path+"/decision", first.decision, "", otherLogin, 404)
	call("POST", first.path+"/decision", strings.Replace(first.decision, `,"acknowledge_no_remove":true`, "", 1), "", cookies, 422)
	slot(1, start, false)
	result := call("POST", first.path+"/decision", first.decision, "", cookies, 200)
	var receipt reservationSlotAddWireDetails
	if err := json.Unmarshal(result.Body.Bytes(), &receipt); err != nil || receipt.SlotAdd != nil || receipt.StartAt == nil || *receipt.StartAt != start {
		t.Fatal(result.Body.String(), err)
	}
	approved := call("GET", first.path, "", "", cookies, 200).Body.String()
	if approved != result.Body.String() {
		t.Fatal("browser receipt did not retain exact UTC instant")
	}
	replayed := call("POST", api, body(10, start), token, nil, 200).Body.String()
	if strings.Contains(replayed, "start_at") || !strings.Contains(replayed, "approved") {
		t.Fatal("terminal bearer exposed result fields")
	}
	call("POST", first.path+"/decision", first.decision, "", cookies, 409)
	call("POST", duplicate.path+"/decision", duplicate.decision, "", cookies, 409)
	slot(1, start, true)
	slot(2, start, true)
	if fingerprint(true) != before {
		t.Fatal("slot add changed venue settings, tables, existing requests or private rows")
	}

	otherTime := future.Add(90 * time.Minute)
	competing := queue(12, otherTime)
	call("POST", native, fmt.Sprintf(`{"start_at":%q}`, utc(otherTime)), "", cookies, 201)
	call("POST", competing.path+"/decision", competing.decision, "", cookies, 409)
	slot(1, utc(otherTime), true)
	rejectTime := future.Add(180 * time.Minute)
	reject := queue(13, rejectTime)
	out := call("POST", reject.path+"/decision", fmt.Sprintf(`{"decision":"reject","digest":%q,"confirm":true}`, reject.digest), "", cookies, 200).Body.String()
	if strings.Contains(out, "start_at") {
		t.Fatal("rejection exposed result fields")
	}
	slot(1, utc(rejectTime), false)

	lateTime := future.Add(270 * time.Minute)
	late := queue(14, lateTime)
	public := "/api/v1/public/reservas/synthetic-table/availability?date=" + future.Format(time.DateOnly) + "&party_size="
	call("GET", public+"4", "", "", nil, 404)
	call("PUT", settings, nativeEnable, "", cookies, 200)
	call("POST", late.path+"/decision", late.decision, "", cookies, 409)
	call("POST", api, body(15, utc(lateTime)), token, nil, 409)
	slot(1, utc(lateTime), false)
	var available struct{ Slots []reservationSlot }
	fits := call("GET", public+"4", "", "", nil, 200)
	if err := json.Unmarshal(fits.Body.Bytes(), &available); err != nil || len(available.Slots) != 2 ||
		available.Slots[0].StartAt != when || available.Slots[1].StartAt != start ||
		strings.Contains(fits.Body.String(), "Mesa") || strings.Contains(fits.Body.String(), "guest") {
		t.Fatal("unexpected public availability after separate native enable", err)
	}
	tooLarge := call("GET", public+"5", "", "", nil, 200)
	if err := json.Unmarshal(tooLarge.Body.Bytes(), &available); err != nil || len(available.Slots) != 0 {
		t.Fatal("unexpected capacity after addition", err)
	}
	if fingerprint(false) != noSettings {
		t.Fatal("slot or enable changed reservations, tables or private data")
	}

	call("PUT", settings, strings.Replace(nativeEnable, "true", "false", 1), "", cookies, 200)
	// The native settings writer wins the same outlet lock while a
	// separate browser approval is trying to apply a future start.
	racingTime := future.Add(360 * time.Minute)
	racing := queue(17, racingTime)
	nativeTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer nativeTx.Rollback()
	if err := lockPostgresOutlet(ctx, nativeTx, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reservationSettingsSave(httptest.NewRequestWithContext(ctx, "PUT", settings, nil), nativeTx, 1, []byte(nativeEnable)); err != nil {
		t.Fatal(err)
	}
	raceCode := make(chan int, 1)
	go func() {
		req := httptest.NewRequestWithContext(ctx, "POST", "http://example.com"+racing.path+"/decision", strings.NewReader(racing.decision))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		for _, cookie := range cookies {
			req.AddCookie(cookie)
			if cookie.Name == "csrftoken" {
				req.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		raceCode <- out.Code
	}()
	deadline := time.Now().Add(2 * time.Second)
	for db.Stats().InUse < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if db.Stats().InUse < 2 {
		t.Fatal("approval did not overlap native settings transaction")
	}
	if err := nativeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if status := <-raceCode; status != 409 {
		t.Fatal("approval did not recheck the enabled venue", status)
	}
	slot(1, utc(racingTime), false)
	call("PUT", settings, strings.Replace(nativeEnable, "true", "false", 1), "", cookies, 200)

	// A named key can still be valid after a proposal's exact time expires.
	elapsedAt := future.Add(24 * time.Hour)
	now = elapsedAt.Add(-time.Minute)
	token = key(proposalSessionsAuthPath, issue, cookies)
	elapsed := queue(16, elapsedAt)
	if elapsed.expires != utc(elapsedAt) {
		t.Fatal("proposal expiry not bounded by slot start", elapsed.expires)
	}
	now = elapsedAt
	call("POST", elapsed.path+"/decision", elapsed.decision, "", cookies, 410)
	slot(1, utc(elapsedAt), false)
	if retry := call("POST", api, body(16, utc(elapsedAt)), token, nil, 200).Body.String(); !strings.Contains(retry, "expired") {
		t.Fatal("elapsed proposal retry restarted the operation")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1"); err != nil {
		t.Fatal(err)
	}
	call("POST", late.path+"/decision", late.decision, "", cookies, 404)
	if _, err := db.ExecContext(ctx, "INSERT INTO outlet_grants VALUES(1,1); UPDATE memberships SET role='accounting' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	call("POST", proposalSessionsAuthPath, issue, "", cookies, 403)
	call("POST", late.path+"/decision", late.decision, "", cookies, 403)
}

func TestReservationSlotAddNativeGridAndHorizon(t *testing.T) {
	today := time.Date(2030, 5, 20, 20, 0, 0, 0, reservationLocation)
	for _, tt := range []struct {
		start string
		valid bool
	}{
		{"2030-05-21T01:30:00Z", true}, // May 20, 22:30 local.
		{"2030-05-21T03:00:00Z", true}, // May 21, 00:00 local.
		{"2030-05-21T04:30:00Z", true},
		{"2030-05-21T04:31:00Z", false},
		{"2030-05-20T23:00:00Z", false}, // No longer strictly future.
		{"2030-07-20T01:30:00Z", true},  // Local day +60, last grid start.
		{"2030-07-20T03:00:00Z", false}, // Local day +61.
		{"2030-05-20T22:30:00-03:00", false},
		{"2030-05-21T01:30:00.000Z", false},
		{"2030-05-21T01:30:00+00:00", false},
	} {
		_, err := parseReservationSlotAddStart(tt.start)
		if err == nil {
			_, err = reservationStart(tt.start, today)
		}
		if (err == nil) != tt.valid {
			t.Errorf("%s valid=%t expected %t: %v", tt.start, err == nil, tt.valid, err)
		}
	}
}
