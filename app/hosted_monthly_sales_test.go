package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPostgresHostedMonthlySales(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Microsecond)
	var eligibilityClock atomic.Int64
	setClock := func(offset time.Duration) { eligibilityClock.Store(start.Add(offset).UnixMicro()) }
	setClock(0)
	mode, source := newEligibilityFixture(func() time.Time { return time.UnixMicro(eligibilityClock.Load()) })
	const subject = "fixture-hosted-monthly-clerk"
	configure := func(a *app) {
		a.mcpClock = func() time.Time { return time.Date(2026, 12, 1, 1, 30, 0, 0, time.UTC) }
		a.posClock = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
		a.enableStock = true
	}
	f := newDiscoveryTestServerConfigured(t, ctx, db, mode, subject, configure)
	control := newDiscoveryTestServerConfigured(t, ctx, db, nil, subject, configure)
	c, base := f.client(), control.client()
	f.get(c, fixtureDiscoverySignIn, "", 204)
	control.get(base, fixtureDiscoverySignIn, "", 204)
	var actor int64
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM gomar.provider_identities WHERE subject=$1`, subject).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	sql := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	sql(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES
		(701,'QA Entidad A','00000000701',TRUE),(702,'QA Entidad B','00000000702',TRUE),
		(703,'QA Propia','00000000703',TRUE),(704,'QA Propia sin permisos','00000000704',TRUE),
		(705,'QA sin clasificación','00000000705',TRUE);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES
		(711,701,'QA A',TRUE),(721,702,'QA B',TRUE),(722,702,'QA B vacío',TRUE),
		(723,702,'QA sin permiso',TRUE),(724,702,'QA sin mapa',TRUE),
		(725,702,'QA inactivo',FALSE),(726,702,'QA otra cobertura',TRUE),
		(731,703,'QA propio',TRUE),(741,704,'QA propio sin permiso',TRUE),
		(751,705,'QA no clasificado',TRUE);
		INSERT INTO gomar.discovery_entity_class VALUES
		(701,'paid_hosted'),(702,'paid_hosted'),(703,'self_host'),(704,'self_host');
		INSERT INTO gomar.discovery_paid_scope VALUES
		('paid-alpha',ARRAY['scope-branch-a']),('paid-beta',ARRAY['scope-branch-b1','scope-branch-b2','scope-branch-b3','scope-branch-b5']),
		('paid-second',ARRAY['scope-branch-b6']);
		INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref) VALUES
		(711,701,'paid-alpha','scope-branch-a'),(721,702,'paid-beta','scope-branch-b1'),
		(722,702,'paid-beta','scope-branch-b2'),(723,702,'paid-beta','scope-branch-b3'),
		(725,702,'paid-beta','scope-branch-b5'),(726,702,'paid-second','scope-branch-b6')`)
	sql(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active) VALUES
		(701,$1,701,'read_only',TRUE),(702,$1,702,'purchasing',TRUE),(703,$1,703,'read_only',TRUE),
		(704,$1,704,'read_only',TRUE),(705,$1,705,'read_only',TRUE)`, actor)
	sql(`INSERT INTO gomar.outlet_grants VALUES (701,711),(702,721),(702,722),(702,724),(702,725),(702,726),(703,731),(705,751);
		INSERT INTO gomar.pos_products(id,outlet_id,current_revision) VALUES(721,721,1);
		INSERT INTO gomar.pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
		VALUES(721,721,1,'QA local receipt product',17,TRUE);
		INSERT INTO gomar.stock_outlets VALUES(721,TRUE);
		INSERT INTO gomar.stock_products VALUES(721,721,TRUE,0)`)
	sql(`INSERT INTO gomar.pos_sales(request_id,outlet_id,actor_id,total_minor,recorded_at,till_reported_at_utc)
		VALUES
		('87000000-0000-4000-8000-000000000001',711,$1,100,'2026-11-18T12:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000002',721,$1,20,'2026-11-01T03:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000003',721,$1,30,'2026-12-01T02:59:59.999999999Z','2026-12-01T03:01:00Z'),
		('87000000-0000-4000-8000-000000000004',721,$1,90,'2026-12-01T03:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000005',721,$1,15,'2026-11-01T02:59:59.999999999Z',NULL),
		('87000000-0000-4000-8000-000000000006',723,$1,1111,'2026-11-18T12:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000007',724,$1,2222,'2026-11-18T12:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000008',725,$1,3333,'2026-11-18T12:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000009',726,$1,4444,'2026-11-18T12:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000010',731,$1,700,'2026-11-18T12:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000011',741,$1,5555,'2026-11-18T12:00:00Z',NULL),
		('87000000-0000-4000-8000-000000000012',751,$1,6666,'2026-11-18T12:00:00Z',NULL)`, actor)
	// Exercise the unmodified writer with one exact logged-in retry before
	// measuring the real readers. One receipt and one stock debit must remain.
	queued := fmt.Sprintf(`{"id_solicitud":"87000000-0000-4000-8000-000000000013","actor_id":%d,"items":[{"producto_id":721,"revision":1,"cantidad":1}],"metodo_pago":"efectivo"}`, actor)
	path := "/api/v1/entities/702/outlets/721/pos/sales"
	first := f.request(c, http.MethodPost, path, queued, "", 201)
	if !bytes.Equal(first, f.request(c, http.MethodPost, path, queued, "", 200)) {
		t.Fatal("same offline request did not retain its original receipt")
	}
	snapshot := func() string {
		t.Helper()
		var state string
		var sales, items, stock int
		if err := db.QueryRowContext(ctx, `SELECT jsonb_build_array(
			(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM gomar.pos_sales p),
			(SELECT jsonb_agg(to_jsonb(i) ORDER BY sale_id,product_id) FROM gomar.pos_sale_items i),
			(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM gomar.stock_movements m))::text,
			(SELECT count(*) FROM gomar.pos_sales),(SELECT count(*) FROM gomar.pos_sale_items),
			(SELECT count(*) FROM gomar.stock_movements WHERE outlet_id=721 AND product_id=721 AND kind='venta' AND delta=-1)`).
			Scan(&state, &sales, &items, &stock); err != nil || sales != 13 || items != 1 || stock != 1 {
			t.Fatalf("fixture lost the original receipt or exact stock effect: %d %d %d (%v)", sales, items, stock, err)
		}
		return state
	}
	before := snapshot()
	aBranches, bBranches := []string{"scope-branch-a"}, []string{"scope-branch-b1", "scope-branch-b2", "scope-branch-b3", "scope-branch-b5"}
	apply := func(scope string, branches []string, epoch int64, state string, from, until time.Duration, expect hostedEligibilityApplyResult) {
		t.Helper()
		source.apply(t, ctx, f.app, fixtureHostedEligibility(scope, branches, epoch, state, start.Add(from), start.Add(until)), expect)
	}
	apply("paid-alpha", aBranches, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)
	apply("paid-beta", bBranches, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)
	type lane struct {
		server  discoveryTestServer
		token   string
		session *mcp.ClientSession
	}
	issue := func(server discoveryTestServer, c *http.Client, entity, outlet int64) lane {
		t.Helper()
		binding := map[string]int64{"entity_id": entity}
		if outlet > 0 {
			binding["outlet_id"] = outlet
		}
		body, _ := json.Marshal(binding)
		var out struct{ Token string }
		if json.Unmarshal(server.request(c, http.MethodPost, "/api/v1/auth/mcp-token", string(body), "", 201), &out) != nil || !providerRandom(out.Token) {
			t.Fatal("could not issue existing exact read credential")
		}
		return lane{server, out.Token, openHostedMonthlyMCP(t, ctx, server, out.Token)}
	}
	ar, ap, br, bz := issue(f, c, 701, 0), issue(f, c, 701, 711), issue(f, c, 702, 0), issue(f, c, 702, 722)
	cSelf, cEmpty, legacy, unclassified := issue(f, c, 703, 0), issue(f, c, 704, 0), issue(control, base, 702, 0), issue(f, c, 705, 0)
	legacyA := lane{control, ar.token, openHostedMonthlyMCP(t, ctx, control, ar.token)}
	check := func(l lane, entity, outlet int64, period string, missing *problem, want ...mcpSalesOutlet) {
		t.Helper()
		path := fmt.Sprintf("/api/v1/mcp/entities/%d/sales-summary?period=%s", entity, period)
		args := map[string]any{"entity_id": entity, "period": period}
		if outlet > 0 {
			path += fmt.Sprintf("&outlet_id=%d", outlet)
			args["outlet_id"] = outlet
		}
		status := 200
		if missing != nil {
			status = missing.status
		}
		wire := l.server.get(l.server.server.Client(), path, l.token, status)
		if missing != nil {
			var payload map[string]string
			if json.Unmarshal(wire, &payload) != nil || len(payload) != 1 || payload["detail"] != missing.detail {
				t.Fatal("denied aggregate disclosed more than the generic scoped denial")
			}
		} else {
			from, to := "2026-11-01T00:00:00-03:00", "2026-12-01T00:00:00-03:00"
			if period == "previous_month" {
				from, to = "2026-10-01T00:00:00-03:00", from
			}
			wantReport := mcpSales{Period: period, From: from, To: to, Timezone: "America/Argentina/Buenos_Aires", Currency: "ARS", Outlets: append([]mcpSalesOutlet{}, want...)}
			for _, row := range want {
				wantReport.SaleCount += row.SaleCount
				wantReport.TotalMinor += row.TotalMinor
			}
			var got mcpSales
			if json.Unmarshal(wire, &got) != nil || !reflect.DeepEqual(got, wantReport) {
				t.Fatalf("wrong exact monthly summary: got %+v, want %+v", got, wantReport)
			}
		}
		if l.session != nil {
			r, err := l.session.CallTool(ctx, &mcp.CallToolParams{Name: "gomar_sales_summary", Arguments: args})
			if err != nil || r == nil || r.IsError != (missing != nil) {
				t.Fatalf("packaged summary disagreed with app: %v", err)
			}
			var got, expect any
			if missing != nil {
				text, _ := json.Marshal(r.Content)
				for _, denied := range []string{"sale_count", "total_minor", "outlets", "QA ", "paid-", "scope-branch", "1111", "2222", "3333", "4444", "5544"} {
					if r.StructuredContent != nil || strings.Contains(string(text), denied) {
						t.Fatal("packaged denial leaked an outlet, private binding or financial evidence")
					}
				}
			} else {
				result, err := json.Marshal(r.StructuredContent)
				if err != nil || json.Unmarshal(wire, &expect) != nil || json.Unmarshal(result, &got) != nil || !reflect.DeepEqual(got, expect) {
					t.Fatal("packaged structured output deviated from the precise same-scope report")
				}
			}
		}
	}
	row := func(id int64, name string, count, minor int64) mcpSalesOutlet {
		return mcpSalesOutlet{mcpPlace{id, name}, count, minor}
	}
	aSale, bSale, zero, self := row(711, "QA A", 1, 100), row(721, "QA B", 3, 67), row(722, "QA B vacío", 0, 0), row(731, "QA propio", 1, 700)
	check(ar, 701, 0, "this_month", nil, aSale)
	check(ap, 701, 0, "this_month", nil, aSale)
	check(ap, 701, 711, "this_month", nil, aSale)
	check(ap, 701, 721, "this_month", &missingOutlet)
	check(ap, 702, 0, "this_month", &missingEntity)
	regressB := func() {
		t.Helper()
		check(br, 702, 0, "this_month", nil, bSale, zero)
		check(br, 702, 721, "this_month", nil, bSale)
		check(br, 702, 722, "this_month", nil, zero)
		check(bz, 702, 0, "this_month", nil, zero)
		check(bz, 702, 721, "this_month", &missingOutlet)
		check(br, 702, 0, "previous_month", nil, row(721, "QA B", 1, 15), zero)
		for _, id := range []int64{723, 724, 725, 726, 731} {
			check(br, 702, id, "this_month", &missingOutlet)
		}
	}
	regressB()
	check(unclassified, 705, 0, "this_month", &missingEntity)
	check(cSelf, 703, 0, "this_month", nil, self)
	check(cEmpty, 704, 0, "this_month", nil)
	check(cEmpty, 704, 741, "this_month", &missingOutlet)
	check(legacy, 702, 0, "this_month", nil, bSale, zero, row(724, "QA sin mapa", 1, 2222), row(726, "QA otra cobertura", 1, 4444))
	apply("paid-alpha", aBranches, 3, "suspended", 0, 4*time.Second, hostedEligibilityRecorded)
	apply("paid-alpha", aBranches, 2, "eligible", -time.Millisecond, 4*time.Second, hostedEligibilityOlderPositive)
	blockedA := func() {
		t.Helper()
		check(ar, 701, 0, "this_month", &missingEntity)
		check(ar, 701, 711, "this_month", &missingOutlet)
		check(ap, 701, 0, "this_month", &missingOutlet)
		check(ap, 701, 711, "this_month", &missingOutlet)
		check(legacyA, 701, 0, "this_month", nil, aSale)
		regressB()
		check(cSelf, 703, 0, "this_month", nil, self)
		check(cEmpty, 704, 0, "this_month", nil)
	}
	blockedA()
	setClock(time.Second)
	apply("paid-alpha", aBranches, 4, "eligible", time.Second, 2*time.Second, hostedEligibilityRecorded)
	check(ar, 701, 0, "this_month", nil, aSale)
	check(ap, 701, 0, "this_month", nil, aSale)
	setClock(2 * time.Second)
	blockedA()
	if snapshot() != before {
		t.Fatal("monthly summaries changed receipts, items or stock movements")
	}
	if os.Getenv("GOMAR_QA_HOSTED_MONTHLY_MCP") != "" {
		t.Log("PASS: actual packaged MCP monthly summaries held exact root/pin across suspension and expiry, valid zero and self-host intact")
	}
}

func openHostedMonthlyMCP(t *testing.T, ctx context.Context, f discoveryTestServer, token string) *mcp.ClientSession {
	t.Helper()
	binary := os.Getenv("GOMAR_QA_HOSTED_MONTHLY_MCP")
	if binary == "" {
		return nil
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("require the fresh absolute packaged MCP binary")
	}
	scratch := t.TempDir()
	cert := filepath.Join(scratch, "fixture.pem")
	key := filepath.Join(scratch, "fixture.key")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "--origin", f.server.URL, "--token-file", key)
	cmd.Env = []string{"SSL_CERT_FILE=" + cert, "LANG=C.UTF-8", "HOME=" + scratch}
	client := mcp.NewClient(&mcp.Implementation{Name: "gomar-hosted-monthly-qa", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("could not connect actual packaged MCP: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error("packaged MCP did not close cleanly")
		}
	})
	return session
}
