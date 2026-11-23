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

func TestPostgresMCPReservationTableAddWorkflow(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	const password = "synthetic-reservation-table-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,username,password,active,staff,superuser)
 VALUES(1,'reservation-table-workflow',$1,TRUE,FALSE,FALSE)`, encodePassword(password)); err != nil {
		t.Fatal(err)
	}
	future := time.Now().In(reservationLocation).AddDate(0, 0, 2)
	future = time.Date(future.Year(), future.Month(), future.Day(), 19, 0, 0, 0, reservationLocation)
	if _, err := db.ExecContext(ctx, `
 INSERT INTO entities(id,name,cuit,active) VALUES(1,'Fixture entity','00000000000',TRUE),(2,'Another fixture','00000000001',TRUE);
 INSERT INTO outlets(id,entity_id,name,active) VALUES(1,1,'A',TRUE),(2,1,'B',TRUE),(3,2,'C',TRUE),(4,1,'D',TRUE);
 INSERT INTO memberships(id,user_id,entity_id,role,active) VALUES(1,1,1,'administration',TRUE);
 INSERT INTO outlet_grants VALUES(1,1),(1,2),(1,4);
 INSERT INTO reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled,collect_contact) VALUES
 (1,'synthetic-table','Fixture venue','Private policy sentinel',FALSE,TRUE),
 (2,'synthetic-other','Other venue','Second policy',FALSE,FALSE);
 INSERT INTO reservation_tables(outlet_id,label,capacity) VALUES(1,'Mesa 1',2),(1,'MESA  007',2),(2,'Mesa 2',4);`); err != nil {
		t.Fatal(err)
	}
	when := future.UTC().Format(time.RFC3339)
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
	a := app{db: db, host: "example.com", enableMCP: true, publicReservations: true, reservationContacts: true, enableMCPReservationTableAddProposals: true}
	h := a.routes()
	cookies := postgresTestCookies(t, h, "reservation-table-workflow", password)
	otherLogin := postgresTestCookies(t, h, "reservation-table-workflow", password)
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

	const api = "/api/v1/mcp/entities/1/outlets/1/reservation-table-add-proposals"
	const browser = "/api/v1/entities/1/outlets/1/reservation-table-add-proposals"
	const settings = "/api/v1/entities/1/outlets/1/reservations/settings"
	const native = "/api/v1/entities/1/outlets/1/reservations/tables"
	const issue = `{"entity_id":1,"outlet_id":1,"actions":["reservation_table_add"]}`
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
	body := func(id, number, capacity int) string {
		return fmt.Sprintf(`{"request_id":"55555555-0000-4000-8000-%012d","table_add":{"table_number":%d,"capacity":%d}}`, id, number, capacity)
	}
	call("POST", api, body(1, 2, 4), read, nil, 401)
	call("POST", api, body(1, 2, 4), token, cookies, 422)
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/2/", 1), body(1, 2, 4), token, nil, 404)
	call("POST", strings.Replace(api, "/entities/1/", "/entities/2/", 1), body(1, 2, 4), token, nil, 404)
	for _, invalid := range []string{body(1, 0, 4), body(1, 10000, 4), body(1, 2, 0), body(1, 2, 13),
		strings.Replace(body(1, 2, 4), `"table_number":2`, `"label":"Person"`, 1),
		strings.Replace(body(1, 2, 4), `"capacity":4`, `"capacity":null`, 1)} {
		call("POST", api, invalid, token, nil, 422)
	}
	call("POST", api, body(1, 1, 4), token, nil, 409)
	call("POST", api, body(1, 7, 4), token, nil, 409)
	unconfigured := key(proposalSessionsAuthPath, strings.Replace(issue, `"outlet_id":1`, `"outlet_id":4`, 1), cookies)
	call("POST", strings.Replace(api, "/outlets/1/", "/outlets/4/", 1), body(1, 2, 4), unconfigured, nil, 404)
	fingerprint := func(includeSettings bool) string {
		t.Helper()
		var digest string
		err := db.QueryRowContext(ctx, `SELECT md5(concat(
  (SELECT json_agg(x ORDER BY outlet_id)::text FROM reservation_venues x WHERE $1),
  (SELECT json_agg(x ORDER BY outlet_id,start_at)::text FROM reservation_slots x),
  (SELECT json_agg(x ORDER BY id)::text FROM reservation_requests x),
  (SELECT json_agg(x ORDER BY request_id)::text FROM reservation_request_policies x),
  (SELECT json_agg(x ORDER BY id)::text FROM reservation_customers x),
  (SELECT json_agg(x ORDER BY reservation_id)::text FROM reservation_private_contacts x),
  (SELECT json_agg(x ORDER BY outlet_id,label)::text FROM reservation_tables x WHERE outlet_id<>1)
 ))`, includeSettings).Scan(&digest)
		if err != nil || digest == "" {
			t.Fatal("cannot snapshot native reservation facts")
		}
		return digest
	}
	table := func(outlet int, label string, want int) {
		t.Helper()
		var count, got int
		err := db.QueryRowContext(ctx, `SELECT count(*),coalesce(max(capacity),0)
 FROM reservation_tables WHERE outlet_id=$1 AND label=$2`, outlet, label).Scan(&count, &got)
		if err != nil || got != want || (want == 0) != (count == 0) {
			t.Fatal(label, got, count, err)
		}
	}
	type queued struct{ path, decision, digest string }
	queue := func(id, number, capacity int) queued {
		t.Helper()
		raw := body(id, number, capacity)
		first := call("POST", api, raw, token, nil, 201)
		if retry := call("POST", api, raw, token, nil, 200).Body.String(); retry != first.Body.String() ||
			strings.Contains(retry, "table_label") || strings.Contains(retry, "capacity") {
			t.Fatal("unbounded pending proposal receipt")
		}
		var receipt reservationTableAddWireReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil || receipt.ID <= 0 || receipt.Status != "pending" {
			t.Fatal(receipt, err)
		}
		path := fmt.Sprintf("%s/%d", browser, receipt.ID)
		call("GET", path, "", "", otherLogin, 404)
		var detail reservationTableAddWireDetails
		wire := call("GET", path, "", "", cookies, 200).Body.String()
		for _, secret := range []string{"policy", "synthetic-table", "guest", "note", "+549", "MESA  007", "reservation_reference"} {
			if strings.Contains(wire, secret) {
				t.Fatal("private field appeared in preview:", secret)
			}
		}
		if err := json.Unmarshal([]byte(wire), &detail); err != nil || detail.TableAdd == nil ||
			detail.TableAdd.TableNumber != number || detail.TableAdd.Label != fmt.Sprintf("Mesa %d", number) ||
			detail.TableAdd.Capacity != capacity || detail.TableAdd.VenueEnabled || len(detail.Digest) != 64 {
			t.Fatal(detail, err)
		}
		return queued{path, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true,"acknowledge_no_remove":true}`, detail.Digest), detail.Digest}
	}
	before, noSettings := fingerprint(true), fingerprint(false)
	first, duplicate := queue(10, 2, 4), queue(11, 2, 6)
	call("POST", api, body(10, 2, 4), separate, nil, 409)
	call("POST", first.path+"/decision", first.decision, "", otherLogin, 404)
	call("POST", first.path+"/decision", strings.Replace(first.decision, `,"acknowledge_no_remove":true`, "", 1), "", cookies, 422)
	call("POST", first.path+"/decision", strings.Replace(first.decision, `"acknowledge_no_remove":true`, `"acknowledge_no_remove":false`, 1), "", cookies, 422)
	table(1, "Mesa 2", 0)
	result := call("POST", first.path+"/decision", first.decision, "", cookies, 200)
	var receipt reservationTableAddWireDetails
	if err := json.Unmarshal(result.Body.Bytes(), &receipt); err != nil || receipt.TableAdd != nil || receipt.TableLabel == nil ||
		*receipt.TableLabel != "Mesa 2" || receipt.Capacity == nil || *receipt.Capacity != 4 {
		t.Fatal(result.Body.String(), err)
	}
	approved := call("GET", first.path, "", "", cookies, 200).Body.String()
	if approved != result.Body.String() {
		t.Fatal("browser receipt did not retain exact table")
	}
	replayed := call("POST", api, body(10, 2, 4), token, nil, 200).Body.String()
	if strings.Contains(replayed, "table_label") || strings.Contains(replayed, "capacity") || !strings.Contains(replayed, "approved") {
		t.Fatal("terminal bearer exposed result fields")
	}
	call("POST", first.path+"/decision", first.decision, "", cookies, 409)
	call("POST", duplicate.path+"/decision", duplicate.decision, "", cookies, 409)
	table(1, "Mesa 2", 4)
	table(2, "Mesa 2", 4)
	if fingerprint(true) != before {
		t.Fatal("table add changed venue settings, old reservations, slots or private rows")
	}

	competing := queue(12, 3, 5)
	call("POST", native, `{"label":"Mesa 3","capacity":2}`, "", cookies, 201)
	call("POST", competing.path+"/decision", competing.decision, "", cookies, 409)
	table(1, "Mesa 3", 2)
	reject := queue(13, 4, 5)
	out := call("POST", reject.path+"/decision", fmt.Sprintf(`{"decision":"reject","digest":%q,"confirm":true}`, reject.digest), "", cookies, 200).Body.String()
	if strings.Contains(out, "table_label") || strings.Contains(out, "capacity") {
		t.Fatal("rejection exposed table fields")
	}
	table(1, "Mesa 4", 0)

	blocked := queue(14, 5, 10)
	public := "/api/v1/public/reservas/synthetic-table/availability?date=" + future.Format(time.DateOnly) + "&party_size="
	call("GET", public+"4", "", "", nil, 404)
	call("PUT", settings, nativeEnable, "", cookies, 200)
	call("POST", blocked.path+"/decision", blocked.decision, "", cookies, 409)
	call("POST", api, body(15, 6, 8), token, nil, 409)
	table(1, "Mesa 5", 0)
	var available struct{ Slots []reservationSlot }
	fits := call("GET", public+"4", "", "", nil, 200)
	if err := json.Unmarshal(fits.Body.Bytes(), &available); err != nil || len(available.Slots) != 1 || available.Slots[0].StartAt != when ||
		strings.Contains(fits.Body.String(), "Mesa") || strings.Contains(fits.Body.String(), "guest") {
		t.Fatal("unexpected availability after separate native enable", err)
	}
	tooLarge := call("GET", public+"5", "", "", nil, 200)
	if err := json.Unmarshal(tooLarge.Body.Bytes(), &available); err != nil || len(available.Slots) != 0 {
		t.Fatal("unapproved table changed availability", err)
	}
	// Model the native settings transaction winning the same outlet lock
	// before a separate browser approval reaches the critical section.
	call("PUT", settings, strings.Replace(nativeEnable, "true", "false", 1), "", cookies, 200)
	racing := queue(16, 8, 9)
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
		t.Fatal("browser approval did not start while native settings transaction was held")
	}
	if err := nativeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if status := <-raceCode; status != 409 {
		t.Fatal("approval did not recheck the now-enabled venue after acquiring the outlet", status)
	}
	table(1, "Mesa 8", 0)

	if fingerprint(false) != noSettings {
		t.Fatal("table or enable touched existing requests, policies, slots or contacts")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM outlet_grants WHERE membership_id=1 AND outlet_id=1"); err != nil {
		t.Fatal(err)
	}
	call("POST", blocked.path+"/decision", blocked.decision, "", cookies, 404)
	if _, err := db.ExecContext(ctx, "INSERT INTO outlet_grants VALUES(1,1); UPDATE memberships SET role='accounting' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	call("POST", proposalSessionsAuthPath, issue, "", cookies, 403)
	call("POST", blocked.path+"/decision", blocked.decision, "", cookies, 403)
}
