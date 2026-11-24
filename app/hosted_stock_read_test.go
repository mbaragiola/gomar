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
)

func TestPostgresHostedStockReads(t *testing.T) {
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
	setClock := func(offset time.Duration) { clock.Store(start.Add(offset).UnixMicro()) }
	setClock(0)
	mode, source := newEligibilityFixture(func() time.Time {
		checkedAt := time.UnixMicro(clock.Load())
		if pause.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return checkedAt
	})
	const owner, peer = "fixture-hosted-stock-owner", "fixture-hosted-stock-outsider"
	configure := func(a *app) {
		a.enableStock, a.employeeOnboarding = true, true
		a.posClock = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	}
	f := newDiscoveryTestServerConfigured(t, ctx, db, mode, owner, configure)
	base := newDiscoveryTestServerConfigured(t, ctx, db, nil, owner, configure)
	other := newDiscoveryTestServerConfigured(t, ctx, db, mode, peer, configure)
	browser, ordinary, outside, employee := f.client(), base.client(), other.client(), f.client()
	f.get(browser, fixtureDiscoverySignIn, "", 204)
	base.get(ordinary, fixtureDiscoverySignIn, "", 204)
	other.get(outside, fixtureDiscoverySignIn, "", 204)
	var actor, claimant int64
	if err := db.QueryRowContext(ctx, `SELECT (SELECT user_id FROM gomar.provider_identities WHERE subject=$1),
	 (SELECT user_id FROM gomar.provider_identities WHERE subject=$2)`, owner, peer).Scan(&actor, &claimant); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES
		(701,'QA Stock A','00000000701',TRUE),(702,'QA Stock B','00000000702',TRUE),
		(703,'QA Stock propia','00000000703',TRUE),(704,'QA Stock sin clasificación','00000000704',TRUE);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES
		(711,701,'QA Local A',TRUE),(712,701,'QA Local sin mapa',TRUE),(713,701,'QA Local sin permiso',TRUE),
		(721,702,'QA Local B',TRUE),(722,702,'QA Local vacío',TRUE),(723,702,'QA Control desactivado',TRUE),
		(731,703,'QA Local propio',TRUE),(741,704,'QA Local sin clasificar',TRUE);
		INSERT INTO gomar.discovery_entity_class VALUES (701,'paid_hosted'),(702,'paid_hosted'),(703,'self_host');
		INSERT INTO gomar.discovery_paid_scope VALUES
		 ('paid-alpha',ARRAY['scope-branch-a','scope-branch-ungranted']),
		 ('paid-beta',ARRAY['scope-branch-b','scope-branch-disabled','scope-branch-empty']);
		INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref) VALUES
		(711,701,'paid-alpha','scope-branch-a'),(713,701,'paid-alpha','scope-branch-ungranted'),
		(721,702,'paid-beta','scope-branch-b'),(722,702,'paid-beta','scope-branch-empty'),(723,702,'paid-beta','scope-branch-disabled')`)
	exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active) VALUES
		(701,$1,701,'administration',TRUE),(702,$1,702,'purchasing',TRUE),(703,$1,703,'accounting',TRUE),
		(704,$1,704,'administration',TRUE),(705,$2,702,'purchasing',TRUE);
	`, actor, claimant)
	exec(`INSERT INTO gomar.outlet_grants VALUES(701,711),(701,712),(702,721),(702,722),(702,723),(703,731),(704,741),(705,721);
		INSERT INTO gomar.pos_products(id,outlet_id,current_revision)
		SELECT id,711,1 FROM generate_series(9101,9134) AS id;
		INSERT INTO gomar.pos_products(id,outlet_id,current_revision) VALUES
		(9201,721,1),(9301,731,1),(9401,712,1),(9501,713,1),(9601,723,1),(9701,741,1);
		INSERT INTO gomar.pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
		SELECT id,outlet_id,1,CASE id
		 WHEN 9101 THEN 'QA A Negativo' WHEN 9102 THEN 'QA A Bajo' WHEN 9103 THEN 'QA A Sano'
		 WHEN 9104 THEN 'QA A Producto desactivado' WHEN 9105 THEN 'QA A Solo catálogo'
		 WHEN 9201 THEN 'QA B Visible' WHEN 9301 THEN 'QA Propio Visible' ELSE 'QA producto '||id END,100,TRUE
		FROM gomar.pos_products;
		INSERT INTO gomar.stock_outlets VALUES(711,TRUE),(712,TRUE),(713,TRUE),(721,TRUE),(722,TRUE),(723,FALSE),(731,TRUE),(741,TRUE);
		INSERT INTO gomar.stock_products(outlet_id,product_id,enabled,minimum)
		SELECT outlet_id,id,id<>9104,0 FROM gomar.pos_products WHERE id<>9105`)
	exec(`INSERT INTO gomar.stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
		SELECT 711,9103,$1,'recepcion',2,2,'2026-11-18T03:00:00Z','95000000-0000-4000-8000-' || lpad(n::text,12,'0')
		  FROM generate_series(1,31) AS n;
	`, actor)
	exec(`INSERT INTO gomar.stock_movements(outlet_id,product_id,actor_id,kind,quantity,delta,recorded_at,request_id)
		VALUES(711,9101,$1,'merma',1,-1,'2026-11-18T03:02:00Z','95000000-0000-4000-8000-000000000032'),
		(711,9103,$1,'recepcion',3,3,'2026-11-18T02:59:59.999999999Z','95000000-0000-4000-8000-000000000033'),
		(711,9103,$1,'recepcion',5,5,'2026-11-19T03:00:00Z','95000000-0000-4000-8000-000000000034')`, actor)
	// This setup is a genuine original receipt and retry, not synthetic
	// mutable balance. Stock and sale contents must stay byte-for-byte fixed.
	sale := fmt.Sprintf(`{"id_solicitud":"95000000-0000-4000-8000-000000000035","actor_id":%d,"items":[{"producto_id":9103,"revision":1,"cantidad":1}],"metodo_pago":"efectivo"}`, actor)
	salePath := "/api/v1/entities/701/outlets/711/pos/sales"
	first := f.request(browser, http.MethodPost, salePath, sale, "", 201)
	if !bytes.Equal(first, f.request(browser, http.MethodPost, salePath, sale, "", 200)) {
		t.Fatal("offline retry changed the original receipt")
	}

	// Existing protected employee enrollment/login; the employee is scoped
	// to A and cannot use MCP. The hosted overlay must retain those rules.
	var invite employeeInviteReply
	if err := json.Unmarshal(f.request(browser, http.MethodPost, "/api/v1/entities/701/employees",
		`{"username":"fixture-stock-clerk","role":"purchasing","outlet_ids":[711]}`, "", 201), &invite); err != nil || invite.TemporaryPassword == "" {
		t.Fatal("could not stage synthetic employee")
	}
	f.get(employee, "/api/v1/auth/csrf", "", 200)
	login := func(password string) {
		body, _ := json.Marshal(map[string]any{"organization": 701, "username": invite.Username, "password": password})
		f.request(employee, http.MethodPost, employeeAuthBase+"/login", string(body), "", 200)
	}
	login(invite.TemporaryPassword)
	const password = "Long and disposable fixture stocking passphrase"
	f.request(employee, http.MethodPost, employeeAuthBase+"/password", fmt.Sprintf(`{"password":%q}`, password), "", 200)
	login(password)
	f.request(employee, http.MethodPost, "/api/v1/auth/mcp-token", `{"entity_id":701,"outlet_id":711}`, "", 403)
	f.request(outside, http.MethodPost, "/api/v1/auth/mcp-token", `{"entity_id":701,"outlet_id":711}`, "", 404)

	// A schema-valid dormant install owned by the outside account. It is
	// inert regardless of its planned administration label and branch alias.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	dormant := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	dormant(`INSERT INTO gomar.native_owner_intents VALUES('qa-stock-dormant','paid-alpha','fixture-audience')`)
	dormant(`INSERT INTO gomar.native_owner_designations VALUES
	 ('qa-stock-dormant',1,'qa-stock-dormant-request',$1,$2,ARRAY['scope-branch-a'],NOW(),NOW()+interval '1 hour')`, claimant, actor)
	dormant(`INSERT INTO gomar.native_owner_approvals VALUES
	 ('qa-stock-dormant',1,'approval-'||repeat('a',43),$1,NOW(),NOW()+interval '3 minutes')`, actor)
	dormant(`INSERT INTO gomar.native_owner_previews VALUES
	 (decode(repeat('11',32),'hex'),decode(repeat('22',32),'hex'),'qa-stock-dormant',1,'approval-'||repeat('a',43),NOW(),NOW()+interval '3 minutes');
	 INSERT INTO gomar.native_owner_receipts(receipt_ref,challenge_hash,intent,revision,approval_ref,accepted_at,expires_at,display_digest) VALUES
	 ('accept-'||repeat('b',43),decode(repeat('11',32),'hex'),'qa-stock-dormant',1,'approval-'||repeat('a',43),NOW(),NOW()+interval '2 minutes',decode(repeat('33',32),'hex'))`)
	dormant(`INSERT INTO gomar.native_owner_install_drafts
	 (intent,consent_ref,approved_by,approved_at,legal_name,cuit,initiator_ref,claimant_ref,branch_plan,display_organization) VALUES
	 ('qa-stock-dormant','accept-'||repeat('b',43),$1,NOW(),'QA dormant legal','00000007999','qa-initiator','qa-claimant','[{"Ref":"scope-branch-a","Name":"QA dormant branch"}]','QA dormant display')`, actor)
	dormant(`INSERT INTO gomar.native_owner_pending_installs VALUES
	 ('qa-stock-dormant','accept-'||repeat('b',43),'pending-'||repeat('c',43),'00000007999',$1,'administration',
	 'qa-billing','qa-gomar',decode(repeat('44',32),'hex'),NOW(),NOW()+interval '2 seconds',NOW())`, claimant)
	dormant(`INSERT INTO gomar.native_owner_pending_branches VALUES('pending-'||repeat('c',43),'scope-branch-a','QA dormant branch')`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	issue := func(s discoveryTestServer, c *http.Client, entity, outlet int64) string {
		t.Helper()
		scope := map[string]int64{"entity_id": entity}
		if outlet != 0 {
			scope["outlet_id"] = outlet
		}
		body, _ := json.Marshal(scope)
		var token struct{ Token string }
		if json.Unmarshal(s.request(c, http.MethodPost, "/api/v1/auth/mcp-token", string(body), "", 201), &token) != nil || !providerRandom(token.Token) {
			t.Fatal("could not issue test read credential")
		}
		return token.Token
	}
	ap, ar := issue(f, browser, 701, 711), issue(f, browser, 701, 0)
	br, self, unclass := issue(f, browser, 702, 0), issue(f, browser, 703, 731), issue(f, browser, 704, 741)
	otherKey := issue(other, outside, 702, 721)

	snapshot := func() (data string, keys int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT md5(jsonb_build_array(
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.stock_movements x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY outlet_id,product_id) FROM gomar.stock_products x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY outlet_id) FROM gomar.stock_outlets x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.pos_sales x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY sale_id,product_id) FROM gomar.pos_sale_items x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.entities x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.outlets x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.memberships x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY membership_id,outlet_id) FROM gomar.outlet_grants x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM gomar.access_events x),
			(SELECT jsonb_agg(to_jsonb(x) ORDER BY intent) FROM gomar.native_owner_pending_installs x))::text),
			(SELECT count(*) FROM gomar.mcp_read_tokens)`).Scan(&data, &keys); err != nil {
			t.Fatal(err)
		}
		return data, keys
	}
	aBranches := []string{"scope-branch-a", "scope-branch-ungranted"}
	bBranches := []string{"scope-branch-b", "scope-branch-disabled", "scope-branch-empty"}
	apply := func(scope string, b []string, epoch int64, state string, from, until time.Duration, result hostedEligibilityApplyResult) {
		t.Helper()
		source.apply(t, ctx, f.app, fixtureHostedEligibility(scope, b, epoch, state, start.Add(from), start.Add(until)), result)
	}
	apply("paid-alpha", aBranches, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)
	apply("paid-beta", bBranches, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)
	native := func(e, o int64) string { return fmt.Sprintf("/api/v1/entities/%d/outlets/%d/pos/stock", e, o) }
	mcp := func(e, o int64) string { return fmt.Sprintf("/api/v1/mcp/entities/%d/outlets/%d", e, o) }
	movement := func(p int64) string { return fmt.Sprintf("/products/%d/movements", p) }
	const window = "?start_date=2026-11-18&days=1"
	read := func(s discoveryTestServer, c *http.Client, url, token string, code int) []byte {
		t.Helper()
		response := s.get(c, url, token, code)
		if code >= 400 {
			var payload map[string]string
			if json.Unmarshal(response, &payload) != nil || len(payload) != 1 || payload["detail"] == "" ||
				strings.Contains(string(response), "QA ") || strings.Contains(string(response), "910") {
				t.Fatalf("stock denial disclosed data: %s", response)
			}
		}
		return response
	}
	generic := func(s discoveryTestServer, c *http.Client, url, token string, p problem) {
		t.Helper()
		var response map[string]string
		if json.Unmarshal(read(s, c, url, token, p.status), &response) != nil || response["detail"] != p.detail {
			t.Fatalf("wanted generic %d %q", p.status, p.detail)
		}
	}
	nativeLane := func(e, o, p int64) []string {
		u := native(e, o)
		return []string{u, u + movement(p)}
	}
	mcpLane := func(e, o, p int64) []string {
		u := mcp(e, o)
		move := u + "/stock" + movement(p)
		return []string{u + "/stock", u + "/stock?alert=any", u + fmt.Sprintf("/stock/%d", p),
			u + "/stock-alert-summary", move + "-summary" + window, move + window}
	}
	checkEq := func(url, token string) {
		t.Helper()
		got := read(f, browser, url, token, 200)
		want := read(base, ordinary, url, token, 200)
		if !bytes.Equal(got, want) {
			t.Fatalf("opt-in stock changed admitted result: %s", url)
		}
	}
	checkBAndSelf := func() {
		t.Helper()
		for _, lane := range []struct {
			e, o, p int64
			token   string
		}{{702, 721, 9201, br}, {703, 731, 9301, self}, {702, 723, 9601, br}} {
			for _, url := range nativeLane(lane.e, lane.o, lane.p) {
				checkEq(url, "")
			}
			for _, url := range mcpLane(lane.e, lane.o, lane.p) {
				checkEq(url, lane.token)
			}
		}
	}
	for _, url := range nativeLane(701, 711, 9103) {
		checkEq(url, "")
		if !bytes.Equal(read(f, employee, url, "", 200), read(f, browser, url, "", 200)) {
			t.Fatal("employee lost authorized native stock")
		}
		generic(other, outside, url, "", missingEntity)
	}
	for _, url := range nativeLane(702, 721, 9201) {
		generic(f, employee, url, "", missingEntity)
	}
	for _, url := range mcpLane(701, 711, 9103) {
		checkEq(url, ap)
		checkEq(url, ar)
		generic(other, other.server.Client(), url, otherKey, missingEntity)
	}
	checkBAndSelf()
	for _, lane := range []struct {
		e, o, p int64
		token   string
	}{{701, 712, 9401, ar}, {701, 713, 9501, ar}, {704, 741, 9701, unclass}} {
		for _, url := range nativeLane(lane.e, lane.o, lane.p) {
			generic(f, browser, url, "", missingOutlet)
		}
		for _, url := range mcpLane(lane.e, lane.o, lane.p) {
			generic(f, browser, url, lane.token, missingOutlet)
		}
	}
	for _, url := range mcpLane(701, 712, 9401) {
		generic(f, browser, url, ap, missingOutlet)
	}
	for _, url := range mcpLane(702, 721, 9201) {
		generic(f, browser, url, ap, missingEntity)
	}
	for _, url := range append(nativeLane(701, 711, 9105)[1:], mcp(701, 711)+"/stock/9105",
		mcpLane(701, 711, 9105)[4], mcpLane(701, 711, 9105)[5]) {
		token := ""
		if strings.Contains(url, "/mcp/") {
			token = ap
		}
		generic(f, browser, url, token, posMissingProduct)
	}
	empty := read(f, browser, native(702, 722), "", 200)
	if !bytes.Contains(empty, []byte(`"activo":true`)) || !bytes.Contains(empty, []byte(`"productos":[]`)) {
		t.Fatal("admitted empty active stock not retained")
	}
	for _, url := range []string{mcp(702, 722) + "/stock", mcp(702, 722) + "/stock?alert=low", mcp(702, 722) + "/stock-alert-summary"} {
		checkEq(url, br)
	}
	exec(`UPDATE gomar.memberships SET role='read_only' WHERE id=701`)
	for _, url := range nativeLane(701, 711, 9103) {
		generic(f, browser, url, "", forbiddenRole)
	}
	for _, url := range mcpLane(701, 711, 9103) {
		generic(f, browser, url, ap, forbiddenRole)
	}
	exec(`UPDATE gomar.memberships SET role='administration' WHERE id=701`)
	exec(`DELETE FROM gomar.outlet_grants WHERE (membership_id,outlet_id)=(701,711)`)
	for _, url := range nativeLane(701, 711, 9103) {
		generic(f, browser, url, "", missingOutlet)
	}
	for _, url := range mcpLane(701, 711, 9103) {
		generic(f, browser, url, ap, missingOutlet)
	}
	exec(`INSERT INTO gomar.outlet_grants VALUES(701,711)`)

	// A native GET already after its first scope query must see the old
	// eligibility AND old stock state, not one side of this real race. No
	// new GET may see them after the independently committed suspension.
	checks := func() {
		t.Helper()
		for _, url := range nativeLane(701, 711, 9103) {
			generic(f, browser, url, "", missingOutlet)
			generic(f, employee, url, "", missingOutlet)
			read(base, ordinary, url, "", 200)
		}
		for _, url := range mcpLane(701, 711, 9103) {
			generic(f, browser, url, ap, missingOutlet)
			generic(f, browser, url, ar, missingOutlet)
			read(base, ordinary, url, ap, 200)
		}
		checkBAndSelf()
	}
	wanted := read(f, browser, native(701, 711), "", 200)
	pause.Store(true)
	type waitingStock struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan waitingStock, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+native(701, 711), nil)
		if err != nil {
			done <- waitingStock{err: err}
			return
		}
		response, err := browser.Do(req)
		if err != nil {
			done <- waitingStock{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		done <- waitingStock{response.StatusCode, body, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native read never reached its snapshot")
	}
	apply("paid-alpha", aBranches, 3, "suspended", 0, 4*time.Second, hostedEligibilityRecorded)
	exec(`UPDATE gomar.stock_products SET minimum=66 WHERE product_id=9102 AND outlet_id=711`)
	close(release)
	got := <-done
	if got.err != nil || got.status != 200 || !bytes.Equal(got.body, wanted) ||
		!bytes.Contains(read(base, ordinary, native(701, 711), "", 200), []byte(`"minimo":66`)) {
		t.Fatalf("overlapping scope+stock mutation split the native snapshot: %d %v", got.status, got.err)
	}
	exec(`UPDATE gomar.stock_products SET minimum=0 WHERE product_id=9102 AND outlet_id=711`)
	apply("paid-alpha", aBranches, 2, "eligible", -time.Millisecond, 4*time.Second, hostedEligibilityOlderPositive)
	before, keys := snapshot()
	checks()
	setClock(time.Second)
	apply("paid-alpha", aBranches, 4, "eligible", time.Second, 2*time.Second, hostedEligibilityRecorded)
	for _, url := range nativeLane(701, 711, 9103) {
		checkEq(url, "")
	}
	for _, url := range mcpLane(701, 711, 9103) {
		checkEq(url, ap)
	}
	setClock(2 * time.Second)
	checks()
	testHostedScopedChrome(t, ctx, f, base, "GOMAR_QA_HOSTED_STOCK_BROWSER", "GOMAR_QA_HOSTED_STOCK_MCP",
		"PASS: actual Chrome and all five packaged hosted stock reads", func(phase string) {
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
	if os.Getenv("GOMAR_QA_HOSTED_STOCK_BROWSER") != "" {
		keys += 4 // Four existing synthetic MCP credentials issued explicitly before any transition.
	}
	if data, n := snapshot(); data != before || n != keys {
		t.Fatal("stock readers changed stock, the original receipt, dormant plan or access state")
	}
	t.Log("PASS: stock-only read snapshots, exact scope loss, preserved default/roles and unchanged ledger")
}
