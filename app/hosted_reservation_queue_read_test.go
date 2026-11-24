package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func TestPostgresHostedReservationQueueReads(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Microsecond)
	var clock atomic.Int64
	var pause atomic.Bool
	enters, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	setClock := func(offset time.Duration) { clock.Store(start.Add(offset).UnixMicro()) }
	setClock(0)
	mode, source := newEligibilityFixture(func() time.Time {
		at := time.UnixMicro(clock.Load())
		if pause.CompareAndSwap(true, false) {
			close(enters)
			<-resume
		}
		return at
	})
	const actor, foreign = "fixture-hosted-queue-clerk", "fixture-hosted-queue-outsider"
	config := func(a *app) {
		a.publicReservations, a.reservationContacts = true, true
		a.mcpClock = func() time.Time { return time.Date(2030, 11, 18, 12, 0, 0, 0, time.UTC) }
	}
	f := newDiscoveryTestServerConfigured(t, ctx, db, mode, actor, config)
	base := newDiscoveryTestServerConfigured(t, ctx, db, nil, actor, config)
	other := newDiscoveryTestServerConfigured(t, ctx, db, mode, foreign, config)
	main, normal, outsider := f.client(), base.client(), other.client()
	f.get(main, fixtureDiscoverySignIn, "", 204)
	base.get(normal, fixtureDiscoverySignIn, "", 204)
	other.get(outsider, fixtureDiscoverySignIn, "", 204)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES
		(701,'QA Reservas A','00000000701',TRUE),(702,'QA Reservas B','00000000702',TRUE),
		(703,'QA Reservas propia','00000000703',TRUE);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES
		(711,701,'QA Local A',TRUE),(712,701,'QA sin mapa',TRUE),(713,701,'QA sin permiso',TRUE),
		(721,702,'QA Local B',TRUE),(722,702,'QA Local vacío',TRUE),(731,703,'QA Local propio',TRUE);
		INSERT INTO gomar.discovery_entity_class VALUES (701,'paid_hosted'),(702,'paid_hosted'),(703,'self_host');
		INSERT INTO gomar.discovery_paid_scope VALUES
		 ('paid-alpha',ARRAY['scope-branch-a','scope-branch-ungranted']),
		 ('paid-beta',ARRAY['scope-branch-b','scope-branch-empty']);
		INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref) VALUES
		(711,701,'paid-alpha','scope-branch-a'),(713,701,'paid-alpha','scope-branch-ungranted'),
		(721,702,'paid-beta','scope-branch-b'),(722,702,'paid-beta','scope-branch-empty')`)
	for _, e := range []int64{701, 702, 703} {
		exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
		 SELECT $1,user_id,$1,'administration',TRUE FROM gomar.provider_identities WHERE subject=$2`, e, actor)
	}
	exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
		SELECT 704,user_id,702,'administration',TRUE FROM gomar.provider_identities WHERE subject=$1`, foreign)
	exec(`INSERT INTO gomar.outlet_grants VALUES(701,711),(701,712),(702,721),(702,722),(703,731),(704,721);
		INSERT INTO gomar.reservation_venues(outlet_id,slug,display_name,cancellation_policy,enabled,collect_contact)
		SELECT id,'fixture-venue-'||id,name,'QA aviso original',TRUE,TRUE FROM gomar.outlets;
		INSERT INTO gomar.reservation_tables VALUES(711,'QA Mesa reservada',8);
		INSERT INTO gomar.reservation_slots VALUES(711,'2030-11-20T02:59:59.999999999Z'),(711,'2030-11-20T03:00:00Z'),
		(711,'2030-11-20T12:00:00Z'),(711,'2030-11-20T15:00:00Z'),(711,'2030-11-21T03:00:00Z'),
		(712,'2030-11-20T12:00:00Z'),(713,'2030-11-20T12:00:00Z'),(721,'2030-11-20T12:00:00Z'),(731,'2030-11-20T12:00:00Z');
		INSERT INTO gomar.reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,table_label,created_at) VALUES
		('00000000000000000000000000000001',711,'2030-11-20T02:59:59.999999999Z','QA día anterior',6,'pending',NULL,'2030-11-01T12:00:00Z'),
		('00000000000000000000000000000002',711,'2030-11-20T03:00:00Z','QA Invitada A',2,'pending',NULL,'2030-11-01T12:00:00Z'),
		('00000000000000000000000000000003',711,'2030-11-20T12:00:00Z','QA Invitado confirmado',3,'confirmed','QA Mesa reservada','2030-11-01T12:00:00Z'),
		('00000000000000000000000000000004',711,'2030-11-21T03:00:00Z','QA día siguiente',9,'pending',NULL,'2030-11-01T12:00:00Z'),
		('00000000000000000000000000000005',721,'2030-11-20T12:00:00Z','QA Invitada B',4,'pending',NULL,'2030-11-01T12:00:00Z'),
		('00000000000000000000000000000006',731,'2030-11-20T12:00:00Z','QA Invitada propia',5,'cancelled',NULL,'2030-11-01T12:00:00Z'),
		('00000000000000000000000000000007',712,'2030-11-20T12:00:00Z','QA Invitado sin mapa',11,'pending',NULL,'2030-11-01T12:00:00Z'),
		('00000000000000000000000000000008',713,'2030-11-20T12:00:00Z','QA Invitado sin permiso',12,'pending',NULL,'2030-11-01T12:00:00Z');
		INSERT INTO gomar.reservation_requests(id,outlet_id,start_at,guest_name,party_size,status,created_at)
		 SELECT lpad(to_hex(i),32,'0'),711,'2030-11-20T15:00:00Z','QA lista paginada',1,'cancelled','2030-11-01T12:00:00Z'
		 FROM generate_series(100,129) i;
		INSERT INTO gomar.reservation_customers(id,entity_id,phone_e164)
		 VALUES('5bf11003-a5d6-4aaa-a194-ef61baab4110',701,'+12025550123');
		INSERT INTO gomar.reservation_private_contacts VALUES
		('00000000000000000000000000000002',711,701,'5bf11003-a5d6-4aaa-a194-ef61baab4110','QA no proyectar esta nota')`)
	const (
		day     = "?date=2030-11-20"
		refA    = "00000000000000000000000000000002"
		refB    = "00000000000000000000000000000005"
		refSelf = "00000000000000000000000000000006"
	)
	native := func(e, o int64) string { return fmt.Sprintf("/api/v1/entities/%d/outlets/%d/reservations", e, o) }
	workspace := func(e, o int64) string { return fmt.Sprintf("/api/v1/mcp/entities/%d/outlets/%d", e, o) }
	mcp := func(e, o int64, ref string) []string {
		w := workspace(e, o)
		return []string{w + "/reservation-requests" + day, w + "/reservation-requests/" + ref + day, w + "/reservation-status" + day}
	}
	issue := func(s discoveryTestServer, c *http.Client, e, o int64) string {
		t.Helper()
		scope := map[string]int64{"entity_id": e}
		if o != 0 {
			scope["outlet_id"] = o
		}
		body, _ := json.Marshal(scope)
		var result struct{ Token string }
		if json.Unmarshal(s.request(c, http.MethodPost, "/api/v1/auth/mcp-token", string(body), "", 201), &result) != nil || !providerRandom(result.Token) {
			t.Fatal("no existing MCP read credential")
		}
		return result.Token
	}
	aPin, aRoot, bRoot := issue(f, main, 701, 711), issue(f, main, 701, 0), issue(f, main, 702, 0)
	self, outsiderB := issue(f, main, 703, 731), issue(other, outsider, 702, 721)
	aBranches, bBranches := []string{"scope-branch-a", "scope-branch-ungranted"}, []string{"scope-branch-b", "scope-branch-empty"}
	apply := func(scope string, b []string, epoch int64, state string, from, until time.Duration, want hostedEligibilityApplyResult) {
		t.Helper()
		source.apply(t, ctx, f.app, fixtureHostedEligibility(scope, b, epoch, state, start.Add(from), start.Add(until)), want)
	}
	apply("paid-alpha", aBranches, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)
	apply("paid-beta", bBranches, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)

	read := func(s discoveryTestServer, c *http.Client, path, key string, status int) []byte {
		t.Helper()
		data := s.get(c, path, key, status)
		raw := string(data)
		for _, private := range []string{"+12025550123", "5bf11003-a5d6-4aaa-a194-ef61baab4110", "QA no proyectar", "phone_e164", "customer_id", "paid-alpha", "scope-branch"} {
			if strings.Contains(raw, private) {
				t.Fatalf("unexpected queue data %q", private)
			}
		}
		if key != "" {
			for _, private := range []string{"QA ", "guest_name", "table_label", "cancellation_policy", "contact", "created_at"} {
				if strings.Contains(raw, private) {
					t.Fatalf("unexpected MCP queue data %q", private)
				}
			}
		}
		return data
	}
	deny := func(s discoveryTestServer, c *http.Client, path, key string, p problem) {
		t.Helper()
		var data map[string]string
		if json.Unmarshal(read(s, c, path, key, p.status), &data) != nil || len(data) != 1 || data["detail"] != p.detail {
			t.Fatalf("wanted only the existing generic %d %q", p.status, p.detail)
		}
	}
	equal := func(path, key string) []byte {
		t.Helper()
		got := read(f, main, path, key, 200)
		if !bytes.Equal(got, read(base, normal, path, key, 200)) {
			t.Fatalf("admitted queue no longer equals default: %s", path)
		}
		return got
	}
	aLane := mcp(701, 711, refA)
	aBrowser := native(701, 711) + day
	old := equal(aBrowser, "")
	var nativeRows struct {
		Requests  []reservationEntry
		Truncated bool
	}
	if json.Unmarshal(old, &nativeRows) != nil || nativeRows.Truncated || len(nativeRows.Requests) != 32 ||
		nativeRows.Requests[0].ID != refA || nativeRows.Requests[0].GuestName != "QA Invitada A" ||
		nativeRows.Requests[1].TableLabel != "QA Mesa reservada" {
		t.Fatal("native day boundary, guest projection or table changed")
	}
	deny(other, outsider, aBrowser, "", missingEntity)
	for _, path := range aLane {
		equal(path, aRoot)
		equal(path, aPin)
		deny(other, outsider, path, outsiderB, missingEntity)
	}
	var page mcpclient.ReservationRequests
	if json.Unmarshal(equal(aLane[0], aPin), &page) != nil || len(page.Requests) != 30 || page.Requests[0].ID != refA ||
		page.Requests[0].StartAt != "2030-11-20T00:00:00-03:00" || page.NextID != "0000000000000000000000000000007f" {
		t.Fatal("wrong first packaged queue page")
	}
	cursor := page.NextID
	page = mcpclient.ReservationRequests{}
	if json.Unmarshal(equal(aLane[0]+"&after_id="+cursor, aRoot), &page) != nil || len(page.Requests) != 2 ||
		page.Requests[1].ID != "00000000000000000000000000000081" || page.NextID != "" {
		t.Fatal("wrong final packaged queue page")
	}
	for _, ref := range []string{refB, "00000000000000000000000000000004"} {
		deny(f, main, mcp(701, 711, ref)[1], aRoot, reservationMissing)
		deny(f, main, aLane[0]+"&after_id="+ref, aRoot, badInput)
	}
	checkCounts := func(data []byte, want [3][2]int64) {
		t.Helper()
		var result mcpclient.ReservationStatus
		if json.Unmarshal(data, &result) != nil || result.Date != "2030-11-20" || result.Timezone != reservationTimezone || len(result.Counts) != 3 {
			t.Fatal("bad day status schema")
		}
		for i, row := range result.Counts {
			if row.Status != []string{"pending", "confirmed", "cancelled"}[i] || [2]int64{row.RequestCount, row.PartySizeTotal} != want[i] {
				t.Fatalf("wrong day aggregate: %+v", result)
			}
		}
	}
	checkCounts(equal(aLane[2], aPin), [3][2]int64{{1, 2}, {1, 3}, {30, 30}})
	checkOther := func() {
		t.Helper()
		for _, pair := range []struct {
			e, o     int64
			ref, key string
		}{{702, 721, refB, bRoot}, {703, 731, refSelf, self}} {
			equal(native(pair.e, pair.o)+day, "")
			for _, path := range mcp(pair.e, pair.o, pair.ref) {
				equal(path, pair.key)
			}
		}
		empty := mcp(702, 722, refB)
		checkCounts(equal(empty[2], bRoot), [3][2]int64{})
		for _, item := range []struct{ path, key string }{{native(702, 722) + day, ""}, {empty[0], bRoot}} {
			if !bytes.Contains(equal(item.path, item.key), []byte(`"requests":[]`)) {
				t.Fatal("admitted empty queue was not an empty list")
			}
		}
		deny(f, main, empty[1], bRoot, reservationMissing)
	}
	checkOther()
	for _, o := range []int64{712, 713} {
		deny(f, main, native(701, o)+day, "", missingOutlet)
		for i, p := range mcp(701, o, fmt.Sprintf("%032x", o-705)) {
			deny(f, main, p, aRoot, missingOutlet)
			deny(f, main, p, aPin, missingOutlet)
			if o == 712 && i != 1 {
				read(base, normal, p, aRoot, 200)
			}
		}
	}
	for _, p := range mcp(702, 721, refB) {
		deny(f, main, p, aPin, missingEntity)
	}
	exec(`UPDATE gomar.memberships SET role='purchasing' WHERE id=701`)
	deny(f, main, aBrowser, "", forbiddenRole)
	for _, p := range aLane {
		deny(f, main, p, aPin, forbiddenRole)
	}
	exec(`UPDATE gomar.memberships SET role='administration' WHERE id=701;
	 DELETE FROM gomar.outlet_grants WHERE (membership_id,outlet_id)=(701,711)`)
	deny(f, main, aBrowser, "", missingOutlet)
	for _, p := range aLane {
		deny(f, main, p, aPin, missingOutlet)
	}
	exec(`INSERT INTO gomar.outlet_grants VALUES(701,711)`)

	// Original availability and settings are intentionally outside this
	// slice; the same credential and native session still see old responses.
	availability := workspace(701, 711) + "/reservation-availability?date=2030-11-20&party_size=2"
	settings := native(701, 711) + "/settings"
	public := "/api/v1/public/reservas/fixture-venue-711"
	oldAvailability := equal(availability, aPin)
	oldSettings := equal(settings, "")
	oldPublic := read(f, main, public, "", 200)
	noScopeA := func() {
		t.Helper()
		deny(f, main, aBrowser, "", missingOutlet)
		for _, p := range aLane {
			deny(f, main, p, aPin, missingOutlet)
			deny(f, main, p, aRoot, missingOutlet)
			read(base, normal, p, aPin, 200)
		}
		// Same outcome before consulting an invalid cursor or known ID.
		deny(f, main, aLane[0]+"&after_id="+refB, aRoot, missingOutlet)
		deny(f, main, mcp(701, 711, "no-such-id")[1], aPin, missingOutlet)
		if !bytes.Equal(old, read(base, normal, aBrowser, "", 200)) ||
			!bytes.Equal(oldAvailability, read(f, main, availability, aPin, 200)) ||
			!bytes.Equal(oldSettings, read(f, main, settings, "", 200)) ||
			!bytes.Equal(oldPublic, read(f, main, public, "", 200)) {
			t.Fatal("default or neighboring read changed")
		}
		checkOther()
	}
	// An actual request already reading its native transaction snapshot
	// sees the old queue, not the later data committed with a suspension.
	pause.Store(true)
	type response struct {
		body   []byte
		status int
		err    error
	}
	inFlight := make(chan response, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+aBrowser, nil)
		if err != nil {
			inFlight <- response{err: err}
			return
		}
		httpResult, err := main.Do(req)
		if err != nil {
			inFlight <- response{err: err}
			return
		}
		defer httpResult.Body.Close()
		body, err := io.ReadAll(io.LimitReader(httpResult.Body, 32<<10))
		inFlight <- response{body, httpResult.StatusCode, err}
	}()
	select {
	case <-enters:
	case <-ctx.Done():
		t.Fatal("in-flight queue did not start its original snapshot")
	}
	apply("paid-alpha", aBranches, 3, "suspended", 0, 4*time.Second, hostedEligibilityRecorded)
	exec(`UPDATE gomar.reservation_requests SET guest_name='QA nombre posterior' WHERE id=$1`, refA)
	close(resume)
	observed := <-inFlight
	if observed.err != nil || observed.status != 200 || !bytes.Equal(observed.body, old) ||
		!bytes.Contains(read(base, normal, aBrowser, "", 200), []byte("QA nombre posterior")) {
		t.Fatal("overlap split reservation admission and data snapshots")
	}
	exec(`UPDATE gomar.reservation_requests SET guest_name='QA Invitada A' WHERE id=$1`, refA)
	apply("paid-alpha", aBranches, 2, "eligible", -time.Millisecond, 4*time.Second, hostedEligibilityOlderPositive)
	snapshot := func() (data string, tokens int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT md5(jsonb_build_array(
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.reservation_requests x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY request_id) FROM gomar.reservation_request_policies x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.reservation_customers x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY reservation_id) FROM gomar.reservation_private_contacts x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY outlet_id,start_at) FROM gomar.reservation_slots x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY outlet_id,label) FROM gomar.reservation_tables x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY outlet_id) FROM gomar.reservation_venues x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.memberships x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY membership_id,outlet_id) FROM gomar.outlet_grants x),
		 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.access_events x))::text),
		 (SELECT count(*) FROM gomar.mcp_read_tokens)`).Scan(&data, &tokens); err != nil {
			t.Fatal(err)
		}
		return data, tokens
	}
	before, keys := snapshot()
	noScopeA()
	setClock(time.Second)
	apply("paid-alpha", aBranches, 4, "eligible", time.Second, 2*time.Second, hostedEligibilityRecorded)
	equal(aBrowser, "")
	for _, p := range aLane {
		equal(p, aPin)
	}
	setClock(2 * time.Second)
	noScopeA()

	testHostedScopedChrome(t, ctx, f, base, "GOMAR_QA_HOSTED_QUEUE_BROWSER", "GOMAR_QA_HOSTED_QUEUE_MCP",
		"PASS: actual native Chrome and all three packaged reservation queue reads", func(phase string) {
			switch phase {
			case "begin":
				setClock(3 * time.Second)
				apply("paid-alpha", aBranches, 10, "eligible", 2*time.Second, 6*time.Second, hostedEligibilityRecorded)
				apply("paid-beta", bBranches, 10, "eligible", 2*time.Second, 7*time.Second, hostedEligibilityRecorded)
			case "suspend":
				apply("paid-alpha", aBranches, 11, "suspended", 3*time.Second, 7*time.Second, hostedEligibilityRecorded)
			case "expire":
				setClock(4 * time.Second)
				apply("paid-alpha", aBranches, 12, "eligible", 4*time.Second, 5*time.Second, hostedEligibilityRecorded)
				setClock(5 * time.Second)
			}
		})
	if os.Getenv("GOMAR_QA_HOSTED_QUEUE_BROWSER") != "" {
		keys += 4
	}
	if data, tokens := snapshot(); data != before || tokens != keys {
		t.Fatal("queue reads changed reservation, contact, role, grant or access state")
	}
	t.Log("PASS: exact queue snapshot, day and cursor boundaries, scoped denials and no state change")
}
