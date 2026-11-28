package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mbaragiola/gomar/app/internal/postime"
)

func TestPostgresHostedPOSSaleTime(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Microsecond)
	var now atomic.Int64
	now.Store(start.UnixMicro())
	mode, source := newEligibilityFixture(func() time.Time { return time.UnixMicro(now.Load()) })
	f := newDiscoveryTestServerForSubject(t, ctx, db, mode, "fixture-hosted-timing-owner")
	base := newDiscoveryTestServerForSubject(t, ctx, db, nil, "fixture-hosted-timing-owner")
	other := newDiscoveryTestServerForSubject(t, ctx, db, mode, "fixture-hosted-timing-other")
	c, control, peer := f.client(), base.client(), other.client()
	f.get(c, fixtureDiscoverySignIn, "", 204)
	base.get(control, fixtureDiscoverySignIn, "", 204)
	other.get(peer, fixtureDiscoverySignIn, "", 204)
	actor := func(subject string) (id int64) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT user_id FROM gomar.provider_identities WHERE subject=$1`, subject).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner := actor("fixture-hosted-timing-owner")
	outsider := actor("fixture-hosted-timing-other")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES
		(701,'QA entity A','00000000701',TRUE),(702,'QA entity B','00000000702',TRUE),
		(703,'QA self-host entity','00000000703',TRUE);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES
		(711,701,'QA outlet A',TRUE),(712,701,'QA unmapped outlet',TRUE),
		(721,702,'QA outlet B',TRUE),(731,703,'QA self-host outlet',TRUE);
		INSERT INTO gomar.discovery_entity_class VALUES (701,'paid_hosted'),(702,'paid_hosted'),(703,'self_host');
		INSERT INTO gomar.discovery_paid_scope VALUES ('paid-alpha',ARRAY['scope-branch-a']),('paid-beta',ARRAY['scope-branch-b']);
		INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref) VALUES
		(711,701,'paid-alpha','scope-branch-a'),(721,702,'paid-beta','scope-branch-b')`)
	exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active) VALUES
		(701,$1,701,'administration',TRUE),(702,$1,702,'purchasing',TRUE),(703,$1,703,'accounting',TRUE),
		(704,$2,701,'administration',TRUE)`, owner, outsider)
	exec(`INSERT INTO gomar.outlet_grants VALUES (701,711),(701,712),(702,721),(703,731),(704,711)`)
	type receipt struct {
		entity, outlet int64
		ref            string
	}
	a := receipt{701, 711, "92000000-0000-4000-8000-000000000071"}
	unmapped := receipt{701, 712, "92000000-0000-4000-8000-000000000072"}
	b := receipt{702, 721, "92000000-0000-4000-8000-000000000073"}
	self := receipt{703, 731, "92000000-0000-4000-8000-000000000074"}
	finalized := receipt{701, 711, "92000000-0000-4000-8000-000000000075"}
	missing := receipt{701, 711, "92000000-0000-4000-8000-000000000076"}
	const claimed, registered = "2026-12-01T02:58:00Z", "2026-12-01T03:07:00Z"
	seedReceipt := func(receipt receipt, till *string) {
		t.Helper()
		exec(`INSERT INTO gomar.pos_sales(request_id,outlet_id,actor_id,total_minor,recorded_at,till_reported_at_utc)
			VALUES ($1,$2,$3,100,$4,$5)`, receipt.ref, receipt.outlet, owner, registered, till)
	}
	till := claimed
	for _, sale := range []receipt{a, unmapped, self, finalized} {
		seedReceipt(sale, &till)
	}
	seedReceipt(b, nil) // The legacy absence must not acquire an invented time.
	exec(`INSERT INTO gomar.pos_table_checks(outlet_id,table_number,actor_id,request_id,request_fingerprint,total_minor)
		VALUES (711,1,$1,'92000000-0000-4000-8000-000000000077',decode(repeat('00',32),'hex'),100)`, owner)
	exec(`INSERT INTO gomar.pos_table_tender_events(outlet_id,check_id,actor_id,request_id,kind,amount_minor,method)
		SELECT 711,id,$1,'92000000-0000-4000-8000-000000000078','record',100,'efectivo' FROM gomar.pos_table_checks`, owner)
	exec(`INSERT INTO gomar.pos_table_finalizations(check_id,outlet_id,table_number,sale_id,tender_revision)
		SELECT c.id,c.outlet_id,c.table_number,s.id,t.id FROM gomar.pos_table_checks c
		JOIN gomar.pos_table_tender_events t ON t.check_id=c.id JOIN gomar.pos_sales s ON s.outlet_id=c.outlet_id
		WHERE s.request_id=$1`, finalized.ref)
	epoch := func(scope string, revision int, state string, checked, expires time.Time) {
		t.Helper()
		exec(`INSERT INTO gomar.discovery_scope_epoch
			SELECT business_ref,branches,$2,$3,$4,$5 FROM gomar.discovery_paid_scope WHERE business_ref=$1`,
			scope, revision, state, checked, expires)
	}
	for _, scope := range []string{"paid-alpha", "paid-beta"} {
		epoch(scope, 1, "eligible", start.Add(-time.Second), start.Add(4*time.Second))
	}
	native := func(s receipt) string {
		return fmt.Sprintf("/api/v1/entities/%d/outlets/%d/pos/sale-times/%s", s.entity, s.outlet, s.ref)
	}
	mcp := func(s receipt) string {
		return fmt.Sprintf("/api/v1/mcp/entities/%d/outlets/%d/pos-sale-times/%s", s.entity, s.outlet, s.ref)
	}
	issue := func(server discoveryTestServer, client *http.Client, binding string) string {
		t.Helper()
		var r struct{ Token string }
		if json.Unmarshal(server.request(client, http.MethodPost, "/api/v1/auth/mcp-token", binding, "", 201), &r) != nil || !providerRandom(r.Token) {
			t.Fatal("fixture did not issue an exact synthetic read credential")
		}
		return r.Token
	}
	aKey := issue(f, c, `{"entity_id":701,"outlet_id":711}`)
	uKey := issue(f, c, `{"entity_id":701,"outlet_id":712}`)
	bKey := issue(f, c, `{"entity_id":702,"outlet_id":721}`)
	selfKey := issue(f, c, `{"entity_id":703,"outlet_id":731}`)
	otherKey := issue(other, peer, `{"entity_id":701,"outlet_id":711}`)
	noPin := issue(f, c, `{"entity_id":701}`)
	check := func(server discoveryTestServer, client *http.Client, sale receipt, token, till, marker string) []byte {
		t.Helper()
		n, m := server.get(client, native(sale), "", 200), server.get(client, mcp(sale), token, 200)
		var got postime.Evidence
		want := postime.Evidence{RequestID: sale.ref, RegisteredAt: registered, TillReportedAt: till,
			TillTimeStatus: marker, HandoffURL: postime.Handoff(sale.entity, sale.outlet, sale.ref)}
		if !bytes.Equal(n, m) || json.Unmarshal(n, &got) != nil || got != want {
			t.Fatal("native/package backend timing or exact handoff differed from its own immutable evidence")
		}
		return n
	}
	recovery := func(sale receipt) string {
		return fmt.Sprintf("/api/v1/entities/%d/outlets/%d/pos/sales/%s", sale.entity, sale.outlet, sale.ref)
	}
	wantA := check(f, c, a, aKey, claimed, postime.Unverified)
	f.get(c, recovery(a), "", 200)
	f.get(c, recovery(unmapped), "", 404)
	f.get(c, recovery(finalized), "", 404)
	other.get(peer, recovery(a), "", 404)

	wantB := check(f, c, b, bKey, "", postime.NotReported)
	wantSelf := check(f, c, self, selfKey, claimed, postime.Unverified)
	check(base, control, unmapped, uKey, claimed, postime.Unverified) // Same PG, no configuration.
	generic := f.get(c, native(missing), "", 404)
	deny := func(got []byte) {
		t.Helper()
		if !bytes.Equal(got, generic) || bytes.Contains(got, []byte("handoff")) || bytes.Contains(got, []byte("till_")) || bytes.Contains(got, []byte("registered_at")) {
			t.Fatal("denial differed from exactly missing timing evidence")
		}
	}
	for _, s := range []struct {
		sale receipt
		key  string
	}{{finalized, aKey}, {unmapped, uKey}} {
		deny(f.get(c, native(s.sale), "", 404))
		deny(f.get(c, mcp(s.sale), s.key, 404))
	}
	deny(other.get(peer, native(a), "", 404))
	deny(other.get(peer, mcp(a), otherKey, 404))
	f.get(c, mcp(a), noPin, 404)
	f.get(c, mcp(unmapped), aKey, 404)
	f.get(c, mcp(b), aKey, 404)
	f.get(c, native(a)+"?extra=1", "", 422)
	bad := a
	bad.ref = "not-a-reference"
	f.get(c, native(bad), "", 422)
	f.get(c, mcp(bad), aKey, 422)
	exec(`UPDATE gomar.memberships SET role='read_only' WHERE id=701`)
	f.get(c, native(a), "", 403)
	f.get(c, mcp(a), aKey, 403)
	exec(`UPDATE gomar.memberships SET role='administration' WHERE id=701`)
	counts := func() (n [8]int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.pos_sales),
			(SELECT count(*) FROM gomar.pos_sale_items),(SELECT count(*) FROM gomar.pos_table_finalizations),
			(SELECT count(*) FROM gomar.stock_movements),(SELECT count(*) FROM gomar.access_events),
			(SELECT count(*) FROM gomar.outlet_grants),(SELECT count(*) FROM gomar.mcp_read_tokens),
			(SELECT count(*) FROM gomar.memberships)`).Scan(&n[0], &n[1], &n[2], &n[3], &n[4], &n[5], &n[6], &n[7]); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := counts()
	blockedA := func() {
		t.Helper()
		deny(f.get(c, recovery(a), "", 404))
		deny(f.get(c, native(a), "", 404))
		deny(f.get(c, mcp(a), aKey, 404))
		if !bytes.Equal(check(f, c, b, bKey, "", postime.NotReported), wantB) ||
			!bytes.Equal(check(f, c, self, selfKey, claimed, postime.Unverified), wantSelf) ||
			!bytes.Equal(check(base, control, a, aKey, claimed, postime.Unverified), wantA) {
			t.Fatal("separate paid, self-host or no-config timing changed")
		}
	}
	epoch("paid-alpha", 3, "suspended", start, start.Add(4*time.Second))
	epoch("paid-alpha", 2, "eligible", start.Add(-time.Millisecond), start.Add(4*time.Second))
	blockedA()
	epoch("paid-alpha", 4, "eligible", start.Add(time.Millisecond), start.Add(time.Second))
	now.Store(start.Add(1100 * time.Millisecond).UnixMicro())
	blockedA()
	if counts() != before {
		t.Fatal("scoped timing made a write")
	}
	apply := func(scope, branch string, epoch int64, state string, checked, ends time.Duration, want hostedEligibilityApplyResult) {
		source.apply(t, ctx, f.app, fixtureHostedEligibility(scope, []string{branch}, epoch, state, start.Add(checked), start.Add(ends)), want)
	}
	testHostedScopedChrome(t, ctx, f, base, "GOMAR_QA_HOSTED_TIMING_BROWSER", "GOMAR_QA_HOSTED_TIMING_MCP", "PASS: actual Chrome and packaged MCP hosted POS sale timing", func(phase string) {
		switch phase {
		case "begin":
			now.Store(start.Add(3 * time.Second).UnixMicro())
			apply("paid-alpha", "scope-branch-a", 10, "eligible", 2*time.Second, 6*time.Second, hostedEligibilityRecorded)
			apply("paid-beta", "scope-branch-b", 10, "eligible", 2*time.Second, 7*time.Second, hostedEligibilityRecorded)
		case "suspend":
			apply("paid-alpha", "scope-branch-a", 11, "suspended", 3*time.Second, 7*time.Second, hostedEligibilityRecorded)
			apply("paid-alpha", "scope-branch-a", 9, "eligible", 2*time.Second, 6*time.Second, hostedEligibilityOlderPositive)
		case "expire":
			now.Store(start.Add(4 * time.Second).UnixMicro())
			apply("paid-alpha", "scope-branch-a", 12, "eligible", 4*time.Second, 5*time.Second, hostedEligibilityRecorded)
			now.Store(start.Add(5500 * time.Millisecond).UnixMicro())
		}
	})
	if os.Getenv("GOMAR_QA_HOSTED_TIMING_BROWSER") != "" {
		before[6] += 4 // Only the exact synthetic browser keys, preissued once.
	}
	if counts() != before {
		t.Fatal("scoped timing browser or MCP changed database state")
	}
}
