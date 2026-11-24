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

func TestPostgresHostedDeliveryReads(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Microsecond)
	var clock atomic.Int64
	var pause atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	setClock := func(d time.Duration) { clock.Store(start.Add(d).UnixMicro()) }
	setClock(0)
	mode, source := newEligibilityFixture(func() time.Time {
		at := time.UnixMicro(clock.Load())
		if pause.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return at
	})
	const owner, stranger = "fixture-hosted-delivery-clerk", "fixture-hosted-delivery-stranger"
	config := func(a *app) { a.enableDelivery, a.employeeOnboarding = true, true }
	hosted := newDiscoveryTestServerConfigured(t, ctx, db, mode, owner, config)
	plain := newDiscoveryTestServerConfigured(t, ctx, db, nil, owner, config)
	foreign := newDiscoveryTestServerConfigured(t, ctx, db, mode, stranger, config)
	disabled := newDiscoveryTestServerConfigured(t, ctx, db, mode, owner, nil)
	browser, ordinary, outsider, off, employee := hosted.client(), plain.client(), foreign.client(), disabled.client(), hosted.client()
	for _, lane := range []struct {
		s discoveryTestServer
		c *http.Client
	}{{hosted, browser}, {plain, ordinary}, {foreign, outsider}, {disabled, off}} {
		lane.s.get(lane.c, fixtureDiscoverySignIn, "", 204)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES
		 (701,'QA Delivery A','00000000701',TRUE),(702,'QA Delivery B','00000000702',TRUE),(703,'QA Delivery propio','00000000703',TRUE);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES
		 (711,701,'QA Local A',TRUE),(712,701,'QA Local sin mapa',TRUE),(713,701,'QA Local sin permiso',TRUE),
		 (721,702,'QA Local B',TRUE),(722,702,'QA Local vacío',TRUE),(731,703,'QA Local propio',TRUE);
		INSERT INTO gomar.discovery_entity_class VALUES (701,'paid_hosted'),(702,'paid_hosted'),(703,'self_host');
		INSERT INTO gomar.discovery_paid_scope VALUES
		 ('paid-alpha',ARRAY['scope-branch-a','scope-branch-ungranted']),('paid-beta',ARRAY['scope-branch-b','scope-branch-empty']);
		INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref) VALUES
		 (711,701,'paid-alpha','scope-branch-a'),(713,701,'paid-alpha','scope-branch-ungranted'),
		 (721,702,'paid-beta','scope-branch-b'),(722,702,'paid-beta','scope-branch-empty');
		INSERT INTO gomar.delivery_sources VALUES
		 ('pedidosya','qa-store-accepted',711),('rappi','qa-store-received',711),('uber_eats','qa-store-cancelled',711),
		 ('uber_eats','qa-store-b',721),('rappi','qa-store-self',731),
		 ('pedidosya','qa-store-unmapped',712),('pedidosya','qa-store-ungranted',713);
		INSERT INTO gomar.delivery_orders(id,provider,external_store_id,external_order_id,outlet_id,accepted_items,cancelled,received_at,updated_at)
		SELECT i, CASE WHEN i=8100 THEN 'uber_eats' WHEN i IN (8001,8101) THEN 'rappi' ELSE 'pedidosya' END,
		     CASE WHEN i=8100 THEN 'qa-store-cancelled' WHEN i IN (8001,8101) THEN 'qa-store-received' ELSE 'qa-store-accepted' END,
		     'qa-order-'||i,711,CASE WHEN i=8100 THEN NULL ELSE '[{"nombre":"QA producto privado","cantidad":2}]' END,
		     i=8100,'2030-11-20T10:00:00Z',CASE WHEN i=8002 THEN '2030-11-20T12:01:00Z' ELSE '2030-11-20T12:00:00Z' END
		FROM generate_series(8001,8101) AS i;
		INSERT INTO gomar.delivery_orders(id,provider,external_store_id,external_order_id,outlet_id,accepted_items,cancelled,received_at,updated_at) VALUES
		 (8201,'uber_eats','qa-store-b','qa-order-b',721,'[{"nombre":"QA producto B","cantidad":1}]',FALSE,'2030-11-20T10:00:00Z','2030-11-20T12:00:00Z'),
		 (8301,'rappi','qa-store-self','qa-order-self',731,'[{"nombre":"QA producto propio","cantidad":3}]',FALSE,'2030-11-20T10:00:00Z','2030-11-20T12:00:00Z'),
		 (8401,'pedidosya','qa-store-unmapped','qa-order-unmapped',712,'[]',FALSE,'2030-11-20T10:00:00Z','2030-11-20T12:00:00Z'),
		 (8501,'pedidosya','qa-store-ungranted','qa-order-ungranted',713,'[]',FALSE,'2030-11-20T10:00:00Z','2030-11-20T12:00:00Z');
		INSERT INTO gomar.delivery_order_events
		 SELECT provider,external_store_id,'qa-event-'||id,id,
		 CASE WHEN cancelled THEN 'cancelled' WHEN provider='rappi' THEN 'received' ELSE 'accepted' END,
		 repeat('ab',32),updated_at FROM gomar.delivery_orders`)
	exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
		 SELECT 701,user_id,701,'administration',TRUE FROM gomar.provider_identities WHERE subject=$1`, owner)
	for _, entity := range []int64{702, 703} {
		exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
			 SELECT $1,user_id,$1,'read_only',TRUE FROM gomar.provider_identities WHERE subject=$2`, entity, owner)
	}
	exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
		SELECT 704,user_id,702,'read_only',TRUE FROM gomar.provider_identities WHERE subject=$1`, stranger)
	exec(`INSERT INTO gomar.outlet_grants VALUES(701,711),(701,712),(702,721),(702,722),(703,731),(704,721)`)

	// The pre-existing native employee login still has its own organization
	// boundary. The ordinary fixture then loses admin but keeps read_only.
	var invite employeeInviteReply
	if json.Unmarshal(hosted.request(browser, http.MethodPost, "/api/v1/entities/701/employees",
		`{"username":"fixture-delivery-clerk","role":"read_only","outlet_ids":[711]}`, "", 201), &invite) != nil || invite.TemporaryPassword == "" {
		t.Fatal("could not stage local employee")
	}
	hosted.get(employee, "/api/v1/auth/csrf", "", 200)
	login := func(password string) {
		body, _ := json.Marshal(map[string]any{"organization": 701, "username": invite.Username, "password": password})
		hosted.request(employee, http.MethodPost, employeeAuthBase+"/login", string(body), "", 200)
	}
	const employeePassword = "Long only-for-test delivery passphrase"
	login(invite.TemporaryPassword)
	hosted.request(employee, http.MethodPost, employeeAuthBase+"/password", fmt.Sprintf(`{"password":%q}`, employeePassword), "", 200)
	login(employeePassword)
	hosted.request(employee, http.MethodPost, "/api/v1/auth/mcp-token", `{"entity_id":701,"outlet_id":711}`, "", 403)
	exec(`UPDATE gomar.memberships SET role='read_only' WHERE id=701`)

	issue := func(s discoveryTestServer, c *http.Client, e, o int64) string {
		t.Helper()
		scope := map[string]int64{"entity_id": e}
		if o != 0 {
			scope["outlet_id"] = o
		}
		body, _ := json.Marshal(scope)
		var token struct{ Token string }
		if json.Unmarshal(s.request(c, http.MethodPost, "/api/v1/auth/mcp-token", string(body), "", 201), &token) != nil || !providerRandom(token.Token) {
			t.Fatal("could not issue the existing synthetic MCP read credential")
		}
		return token.Token
	}
	aPin, aRoot, bRoot := issue(hosted, browser, 701, 711), issue(hosted, browser, 701, 0), issue(hosted, browser, 702, 0)
	self, peer := issue(hosted, browser, 703, 731), issue(foreign, outsider, 702, 721)
	aBranches, bBranches := []string{"scope-branch-a", "scope-branch-ungranted"}, []string{"scope-branch-b", "scope-branch-empty"}
	apply := func(ref string, b []string, epoch int64, state string, from, until time.Duration, want hostedEligibilityApplyResult) {
		t.Helper()
		source.apply(t, ctx, hosted.app, fixtureHostedEligibility(ref, b, epoch, state, start.Add(from), start.Add(until)), want)
	}
	apply("paid-alpha", aBranches, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)
	apply("paid-beta", bBranches, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)

	native := func(e, o int64) string { return fmt.Sprintf("/api/v1/entities/%d/outlets/%d/delivery/orders", e, o) }
	packaged := func(e, o int64) [2]string {
		p := fmt.Sprintf("/api/v1/mcp/entities/%d/outlets/%d", e, o)
		return [2]string{p + "/delivery-orders", p + "/delivery-status-summary"}
	}
	read := func(s discoveryTestServer, c *http.Client, path, key string, want int) []byte {
		t.Helper()
		wire := s.get(c, path, key, want)
		banned := []string{"external_store_id", "qa-store", "qa-event", "contact", "paid-alpha", "branch_ref"}
		if key != "" {
			banned = append(banned, "qa-order", "QA producto", "pedido_externo", "items", "external_order_id")
		}
		for _, p := range banned {
			if bytes.Contains(wire, []byte(p)) {
				t.Fatalf("unexpected delivery data marker %q", p)
			}
		}
		return wire
	}
	deny := func(s discoveryTestServer, c *http.Client, path, key string, expected problem) {
		t.Helper()
		var wire map[string]string
		if json.Unmarshal(read(s, c, path, key, expected.status), &wire) != nil || len(wire) != 1 || wire["detail"] != expected.detail {
			t.Fatalf("wanted generic %d %q", expected.status, expected.detail)
		}
	}
	parity := func(path, key string) []byte {
		t.Helper()
		got := read(hosted, browser, path, key, 200)
		if !bytes.Equal(got, read(plain, ordinary, path, key, 200)) {
			t.Fatalf("changed admitted delivery response %s", path)
		}
		return got
	}
	a, nativeA := packaged(701, 711), native(701, 711)
	before := parity(nativeA, "")
	var inbox struct {
		Orders []deliveryOrder `json:"pedidos"`
	}
	if json.Unmarshal(before, &inbox) != nil || len(inbox.Orders) != 100 || inbox.Orders[0].ID != 8002 ||
		inbox.Orders[1].ID != 8101 || inbox.Orders[2].ID != 8100 || inbox.Orders[99].ID != 8003 ||
		inbox.Orders[1].Status != "recibido" || inbox.Orders[2].Status != "cancelado" || len(inbox.Orders[2].Items) != 0 ||
		inbox.Orders[0].ExternalOrderID != "qa-order-8002" || len(inbox.Orders[0].Items) != 1 || inbox.Orders[0].Items[0].Name != "QA producto privado" {
		t.Fatal("native 100/order/cutoff or native detail projection changed")
	}
	if !bytes.Equal(read(hosted, employee, nativeA, "", 200), before) {
		t.Fatal("employee lost same-org granted read")
	}
	deny(hosted, employee, native(702, 721), "", missingEntity)
	deny(foreign, outsider, nativeA, "", missingEntity)
	for _, p := range a {
		parity(p, aRoot)
		deny(foreign, outsider, p, peer, missingEntity)
	}
	var recent mcpclient.Delivery
	if json.Unmarshal(parity(a[0], aPin), &recent) != nil || len(recent.Orders) != 30 || !recent.Truncated ||
		recent.Orders[0].ID != 8002 || recent.Orders[1].ID != 8101 || recent.Orders[2].Status != "cancelled" ||
		recent.Orders[29].ID != 8073 {
		t.Fatal("packaged recent list changed window or tie order")
	}
	checkCounts := func(wire []byte, want mcpclient.DeliveryStatusSummary) {
		t.Helper()
		var got mcpclient.DeliveryStatusSummary
		if json.Unmarshal(wire, &got) != nil || got != want {
			t.Fatalf("wrong exact delivery window: %+v", got)
		}
	}
	checkCounts(parity(a[1], aPin), mcpclient.DeliveryStatusSummary{WindowLimit: 100, Received: 1, Accepted: 98, Cancelled: 1})
	checkOther := func() {
		t.Helper()
		for _, lane := range []struct {
			e, o int64
			k    string
			want mcpclient.DeliveryStatusSummary
		}{
			{702, 721, bRoot, mcpclient.DeliveryStatusSummary{WindowLimit: 100, Accepted: 1}},
			{703, 731, self, mcpclient.DeliveryStatusSummary{WindowLimit: 100, Received: 1}},
			{702, 722, bRoot, mcpclient.DeliveryStatusSummary{WindowLimit: 100}}} {
			paths := packaged(lane.e, lane.o)
			view, r := parity(native(lane.e, lane.o), ""), parity(paths[0], lane.k)
			checkCounts(parity(paths[1], lane.k), lane.want)
			if lane.o == 722 && (!bytes.Equal(view, []byte("{\"pedidos\":[]}\n")) ||
				!bytes.Equal(r, []byte("{\"orders\":[],\"truncated\":false}\n"))) {
				t.Fatal("admitted empty outlet did not keep valid empty responses")
			}
		}
	}
	checkOther()
	for _, o := range []int64{712, 713} {
		deny(hosted, browser, native(701, o), "", missingOutlet)
		for _, p := range packaged(701, o) {
			deny(hosted, browser, p, aRoot, missingOutlet)
			deny(hosted, browser, p, aPin, missingOutlet)
			if o == 712 {
				read(plain, ordinary, p, aRoot, 200)
			}
		}
	}
	for _, p := range packaged(702, 721) {
		deny(hosted, browser, p, aPin, missingEntity)
	}
	for _, p := range append([]string{nativeA}, a[:]...) {
		token := ""
		if strings.Contains(p, "/mcp/") {
			token = aPin
		}
		disabled.get(off, p, token, 404) // Feature route never registered.
	}
	exec(`UPDATE gomar.memberships SET role='unassigned' WHERE id=701`)
	deny(hosted, browser, nativeA, "", forbiddenRole)
	for _, p := range a {
		deny(hosted, browser, p, aPin, forbiddenRole)
	}
	exec(`UPDATE gomar.memberships SET role='read_only' WHERE id=701;
	   DELETE FROM gomar.outlet_grants WHERE (membership_id,outlet_id)=(701,711)`)
	deny(hosted, browser, nativeA, "", missingOutlet)
	for _, p := range a {
		deny(hosted, browser, p, aPin, missingOutlet)
	}
	exec(`INSERT INTO gomar.outlet_grants VALUES(701,711)`)

	// Force one real in-flight inbox read to span a committed
	// suspension followed by a new stored value. Its original snapshot may
	// complete, but the next request must deny before seeing any order.
	type outcome struct {
		body   []byte
		status int
		err    error
	}
	done := make(chan outcome, 1)
	pause.Store(true)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, hosted.server.URL+nativeA, nil)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		resp, err := browser.Do(req)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		defer resp.Body.Close()
		wire, err := io.ReadAll(io.LimitReader(resp.Body, 48<<10))
		done <- outcome{wire, resp.StatusCode, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native read never set up its scoped snapshot")
	}
	apply("paid-alpha", aBranches, 3, "suspended", 0, 4*time.Second, hostedEligibilityRecorded)
	exec(`UPDATE gomar.delivery_orders SET external_order_id='qa-order-after-commit' WHERE id=8002`)
	close(release)
	pending := <-done
	if pending.err != nil || pending.status != 200 || !bytes.Equal(pending.body, before) ||
		!bytes.Contains(read(plain, ordinary, nativeA, "", 200), []byte("qa-order-after-commit")) {
		t.Fatal("native inbox lost its single snapshot across the overlap")
	}
	exec(`UPDATE gomar.delivery_orders SET external_order_id='qa-order-8002' WHERE id=8002`)
	apply("paid-alpha", aBranches, 2, "eligible", -time.Millisecond, 4*time.Second, hostedEligibilityOlderPositive)

	blockedA := func() {
		t.Helper()
		deny(hosted, browser, nativeA, "", missingOutlet)
		deny(hosted, employee, nativeA, "", missingOutlet)
		if !bytes.Equal(read(plain, ordinary, nativeA, "", 200), before) {
			t.Fatal("default inbox changed")
		}
		for _, p := range a {
			deny(hosted, browser, p, aPin, missingOutlet)
			deny(hosted, browser, p, aRoot, missingOutlet)
			read(plain, ordinary, p, aPin, 200)
		}
		checkOther()
	}
	snapshot := func() (string, int) {
		t.Helper()
		var state string
		var n int
		if err := db.QueryRowContext(ctx, `SELECT md5(jsonb_build_array(
			(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM gomar.delivery_orders d),
			(SELECT jsonb_agg(to_jsonb(d) ORDER BY provider,external_store_id) FROM gomar.delivery_sources d),
			(SELECT jsonb_agg(to_jsonb(d) ORDER BY provider,external_store_id,external_event_id) FROM gomar.delivery_order_events d),
			(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM gomar.memberships d),
			(SELECT jsonb_agg(to_jsonb(d) ORDER BY membership_id,outlet_id) FROM gomar.outlet_grants d),
			(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM gomar.access_events d))::text),
			(SELECT count(*) FROM gomar.mcp_read_tokens)`).Scan(&state, &n); err != nil {
			t.Fatal(err)
		}
		return state, n
	}
	stable, keys := snapshot()
	blockedA()
	setClock(time.Second)
	apply("paid-alpha", aBranches, 4, "eligible", time.Second, 2*time.Second, hostedEligibilityRecorded)
	parity(nativeA, "")
	for _, p := range a {
		parity(p, aPin)
	}
	setClock(2 * time.Second)
	blockedA()
	testHostedScopedChrome(t, ctx, hosted, plain, "GOMAR_QA_HOSTED_DELIVERY_BROWSER", "GOMAR_QA_HOSTED_DELIVERY_MCP",
		"PASS: actual Chrome and both packaged delivery reads", func(phase string) {
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
	if os.Getenv("GOMAR_QA_HOSTED_DELIVERY_BROWSER") != "" {
		keys += 4
	}
	if state, n := snapshot(); state != stable || n != keys {
		t.Fatal("inbox/summary reads changed stores, orders, events or access")
	}
	t.Log("PASS: exact read-only delivery snapshots, projections and committed scope loss")
}
