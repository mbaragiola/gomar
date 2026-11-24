package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type barcodeTestScope struct {
	entity, outlet, product int64
	paid, branch            string
}

func (s barcodeTestScope) native() string {
	return fmt.Sprintf("/api/v1/entities/%d/outlets/%d/pos/catalog/%d/barcode", s.entity, s.outlet, s.product)
}
func (s barcodeTestScope) agent() string {
	return fmt.Sprintf("/api/v1/mcp/entities/%d/outlets/%d/pos-barcode-proposals", s.entity, s.outlet)
}
func (s barcodeTestScope) review() string {
	return fmt.Sprintf("/api/v1/entities/%d/outlets/%d/pos-barcode-proposals", s.entity, s.outlet)
}

func TestPostgresHostedPOSBarcode(t *testing.T) {
	ctx, db := testPostgresDB(t)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Microsecond)
	var tick atomic.Int64
	at := func(offset time.Duration) time.Time { return start.Add(offset) }
	move := func(offset time.Duration) { tick.Store(at(offset).UnixMicro()) }
	move(0)
	mode, source := newEligibilityFixture(func() time.Time { return time.UnixMicro(tick.Load()) })
	const subject = "fixture-hosted-barcode-user"
	configure := func(a *app) { a.enableMCPPOSCatalogProposals = true }
	f := newDiscoveryTestServerConfigured(t, ctx, db, mode, subject, configure)
	control := newDiscoveryTestServerConfigured(t, ctx, db, nil, subject, configure)
	c, base := f.client(), control.client()
	f.get(c, fixtureDiscoverySignIn, "", 204)
	control.get(base, fixtureDiscoverySignIn, "", 204)
	var actor int64
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM gomar.provider_identities WHERE subject=$1`, subject).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES
		(701,'QA Entidad A','00000000701',TRUE),(702,'QA Entidad B','00000000702',TRUE),(703,'QA Entidad propia','00000000703',TRUE);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES
		(711,701,'QA Local A',TRUE),(712,701,'QA Local sin mapa',TRUE),(721,702,'QA Local B',TRUE),(731,703,'QA Local propio',TRUE);
		INSERT INTO gomar.discovery_entity_class VALUES (701,'paid_hosted'),(702,'paid_hosted'),(703,'self_host');
		INSERT INTO gomar.discovery_paid_scope VALUES ('paid-alpha',ARRAY['scope-branch-a']),('paid-beta',ARRAY['scope-branch-b']);
		INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref) VALUES
		(711,701,'paid-alpha','scope-branch-a'),(721,702,'paid-beta','scope-branch-b')`)
	exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
		VALUES (701,$1,701,'administration',TRUE),(702,$1,702,'administration',TRUE),(703,$1,703,'administration',TRUE)`, actor)
	exec(`INSERT INTO gomar.outlet_grants VALUES (701,711),(701,712),(702,721),(703,731);
		INSERT INTO gomar.pos_products(id,outlet_id,current_revision) VALUES(711,711,1),(712,712,1),(721,721,1),(722,721,1),(731,731,1);
		INSERT INTO gomar.pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
		VALUES(711,711,1,'QA Producto A',701,TRUE),(712,712,1,'QA Producto sin mapa',702,TRUE),
		(721,721,1,'QA Producto B',703,TRUE),(722,721,1,'QA Producto de auditoría',705,TRUE),
		(731,731,1,'QA Producto propio',704,TRUE)`)
	a, b, self, unmapped := barcodeTestScope{701, 711, 711, "paid-alpha", "scope-branch-a"}, barcodeTestScope{702, 721, 721, "paid-beta", "scope-branch-b"}, barcodeTestScope{703, 731, 731, "", ""}, barcodeTestScope{701, 712, 712, "", ""}
	observation := func(scope barcodeTestScope, epoch int64, state string, checked, until time.Duration) hostedEligibilityObservation {
		return fixtureHostedEligibility(scope.paid, []string{scope.branch}, epoch, state, at(checked), at(until))
	}
	apply := func(scope barcodeTestScope, epoch int64, state string, checked, until time.Duration, want hostedEligibilityApplyResult) {
		t.Helper()
		source.apply(t, ctx, f.app, observation(scope, epoch, state, checked, until), want)
	}
	apply(a, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)
	apply(b, 1, "eligible", -time.Second, 4*time.Second, hostedEligibilityRecorded)
	ask := func(server discoveryTestServer, client *http.Client, method, path, body, bearer string, want int) []byte {
		t.Helper()
		status, data, err := barcodeFixtureRequest(ctx, server, client, method, path, body, bearer)
		if err != nil || status != want {
			t.Fatalf("%s %s returned %d, want %d: %s (%v)", method, path, status, want, data, err)
		}
		return data
	}
	issue := func(s barcodeTestScope, action string) string {
		t.Helper()
		wire := fmt.Sprintf(`{"entity_id":%d,"outlet_id":%d,"actions":[%q]}`, s.entity, s.outlet, action)
		var response struct{ Token string }
		if json.Unmarshal(ask(f, c, "POST", proposalSessionsAuthPath, wire, "", 201), &response) != nil || !providerRandom(response.Token) {
			t.Fatal("fixture could not issue existing scoped key")
		}
		return response.Token
	}
	aKey, bKey, cKey := issue(a, posBarcodeSessionAction), issue(b, posBarcodeSessionAction), issue(self, posBarcodeSessionAction)
	priceKey := issue(a, posPriceSessionAction)
	snapshot := func() string {
		t.Helper()
		var result string
		err := db.QueryRowContext(ctx, `SELECT jsonb_build_array(
			(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM gomar.pos_products p),
			(SELECT jsonb_agg(to_jsonb(v) ORDER BY product_id,revision) FROM gomar.pos_product_versions v),
			(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM gomar.mcp_pos_catalog_proposals p),
			(SELECT count(*) FROM gomar.access_events),(SELECT count(*) FROM gomar.pos_sales),
			(SELECT count(*) FROM gomar.stock_movements))::text`).Scan(&result)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	revision := func(s barcodeTestScope, barcode string) int64 {
		t.Helper()
		var n int64
		var code string
		var unchanged bool
		if err := db.QueryRowContext(ctx, `SELECT p.current_revision,COALESCE(p.barcode,''),
			v.name=original.name AND v.price_minor=original.price_minor AND v.active=original.active
			FROM gomar.pos_products p JOIN gomar.pos_product_versions v ON (v.product_id,v.revision)=(p.id,p.current_revision)
			JOIN gomar.pos_product_versions original ON (original.product_id,original.revision)=(p.id,1) WHERE p.id=$1`, s.product).Scan(&n, &code, &unchanged); err != nil || code != barcode || !unchanged {
			t.Fatalf("product changed beyond expected barcode: %d %v", n, err)
		}
		return n
	}
	directBody := func(rev int64, code string) string {
		return fmt.Sprintf(`{"revision":%d,"codigo_barras":%q}`, rev, code)
	}
	agentBody := func(s barcodeTestScope, rev int64, code string, n int) string {
		return fmt.Sprintf(`{"request_id":"86000000-0000-4000-8000-%012d","change":{"product_id":%d,"expected_revision":%d,"barcode":%q}}`, n, s.product, rev, code)
	}
	proposal := func(s barcodeTestScope, key, body string) (string, string) {
		t.Helper()
		var result posCatalogWireReceipt
		if err := json.Unmarshal(ask(f, f.server.Client(), "POST", s.agent(), body, key, 201), &result); err != nil || result.ID <= 0 || result.Status != "pending" {
			t.Fatal("could not prepare pending barcode proposal")
		}
		target := fmt.Sprintf("%s/%d", s.review(), result.ID)
		var details posCatalogWireDetails
		if err := json.Unmarshal(ask(f, c, "GET", target, "", "", 200), &details); err != nil || len(details.Digest) != 64 || details.Change == nil {
			t.Fatal("existing private preview was not available")
		}
		return target, fmt.Sprintf(`{"decision":"approve","digest":%q,"confirm":true}`, details.Digest)
	}
	ask(f, c, "PUT", a.native(), directBody(1, "000701"), "", 200)
	aBody := agentBody(a, revision(a, "000701"), "", 1)
	aTarget, aDecision := proposal(a, aKey, aBody)
	ask(f, f.server.Client(), "POST", a.agent(), aBody, aKey, 200)
	ask(f, f.server.Client(), "POST", b.agent(), aBody, aKey, 404)
	ask(f, f.server.Client(), "POST", a.agent(), aBody, priceKey, 401)
	baseline := snapshot()
	generic := ask(f, c, "PUT", unmapped.native(), directBody(1, "000712"), "", 404)
	deny := func(raw []byte) {
		t.Helper()
		if !bytes.Equal(raw, generic) {
			t.Fatal("hosted barcode denial differed from generic no-outlet result")
		}
	}
	deny(ask(f, f.server.Client(), "POST", unmapped.agent(), agentBody(unmapped, 1, "000712", 2), aKey, 404))
	apply(a, 2, "suspended", 0, 4*time.Second, hostedEligibilityRecorded)
	deny(ask(f, c, "PUT", a.native(), directBody(2, "000702"), "", 404))
	deny(ask(f, f.server.Client(), "POST", a.agent(), aBody, aKey, 404))
	deny(ask(f, f.server.Client(), "POST", a.agent(), agentBody(a, 2, "000702", 3), aKey, 404))
	deny(ask(f, c, "GET", a.review(), "", "", 404))
	deny(ask(f, c, "GET", aTarget, "", "", 404))
	deny(ask(f, c, "POST", aTarget+"/decision", aDecision, "", 404))
	if snapshot() != baseline {
		t.Fatal("denied barcode lane made a partial write")
	}
	for _, lane := range []struct {
		s         barcodeTestScope
		key, code string
		n         int
	}{{b, bKey, "000721", 4}, {self, cKey, "000731", 5}} {
		ask(f, c, "PUT", lane.s.native(), directBody(1, lane.code), "", 200)
		target, decision := proposal(lane.s, lane.key, agentBody(lane.s, revision(lane.s, lane.code), "", lane.n))
		ask(f, c, "POST", target+"/decision", decision, "", 200)
		if revision(lane.s, "") != 3 {
			t.Fatal("approved barcode set/clear did not preserve the product")
		}
	}
	// The default app still allows its own original exact write behavior.
	ask(control, base, "PUT", unmapped.native(), directBody(1, "000712"), "", 200)
	// Price and availability keep their existing behavior even on suspended A.
	priceAPI := strings.Replace(a.agent(), "pos-barcode-proposals", "pos-price-proposals", 1)
	ask(f, f.server.Client(), "POST", priceAPI, `{"request_id":"86000000-0000-4000-8000-000000000006","change":{"product_id":711,"expected_revision":2,"price_minor":999}}`, priceKey, 201)
	if revision(a, "000701") != 2 {
		t.Fatal("proposal changed unrelated product")
	}

	// Disposable-only triggers pause real SQL statements; no product code
	// injects waits. Each scenario verifies pg_stat_activity actually waited.
	exec(`CREATE FUNCTION gomar.qa_block_barcode_epoch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.epoch=4 THEN PERFORM pg_advisory_xact_lock(440071); END IF; RETURN NEW; END $$;
		CREATE TRIGGER qa_block_barcode_epoch BEFORE INSERT ON gomar.discovery_scope_epoch FOR EACH ROW EXECUTE FUNCTION gomar.qa_block_barcode_epoch();
		CREATE FUNCTION gomar.qa_block_barcode_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.action='pos_product_barcode_change' AND NEW.entity_id=701 AND NEW.status='approved' THEN
		PERFORM pg_advisory_xact_lock(440072); END IF; RETURN NEW; END $$;
		CREATE TRIGGER qa_block_barcode_receipt BEFORE UPDATE ON gomar.mcp_pos_catalog_proposals FOR EACH ROW EXECUTE FUNCTION gomar.qa_block_barcode_receipt();
		CREATE FUNCTION gomar.qa_block_barcode_access_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.entity_id=702 AND NEW.action='create_membership' THEN PERFORM pg_advisory_xact_lock(440073); END IF;
		RETURN NEW; END $$;
		CREATE TRIGGER qa_block_barcode_access_audit BEFORE INSERT ON gomar.access_events FOR EACH ROW EXECUTE FUNCTION gomar.qa_block_barcode_access_audit()`)
	block := func(key int64) *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { tx.Rollback() })
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	finish := func(tx *sql.Tx) {
		t.Helper()
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	beginApply := func(o hostedEligibilityObservation) <-chan error {
		t.Helper()
		done := make(chan error, 1)
		source.publish(o.binding.business, o)
		go func() {
			r, e := f.app.applyHostedEligibilityObservation(ctx, o.binding.business, o.binding.branches)
			if e == nil && r != hostedEligibilityRecorded {
				e = fmt.Errorf("unexpected apply result: %d", r)
			}
			done <- e
		}()
		return done
	}
	type result struct {
		status int
		body   []byte
		err    error
	}
	beginRequest := func(method, target, body string) <-chan result {
		resultq := make(chan result, 1)
		go func() {
			status, data, e := barcodeFixtureRequest(ctx, f, c, method, target, body, "")
			resultq <- result{status, data, e}
		}()
		return resultq
	}
	finishApply := func(r <-chan error) {
		t.Helper()
		select {
		case e := <-r:
			if e != nil {
				t.Fatal(e)
			}
		case <-ctx.Done():
			t.Fatal("apply did not finish")
		}
	}
	finishDenied := func(r <-chan result) {
		t.Helper()
		select {
		case x := <-r:
			if x.err != nil || x.status != 404 {
				t.Fatalf("race request did not deny: %d %v", x.status, x.err)
			}
			deny(x.body)
		case <-ctx.Done():
			t.Fatal("barcode request did not finish")
		}
	}
	t.Run("suspension_wins_shared_scope_lock", func(t *testing.T) {
		move(time.Second)
		apply(a, 3, "eligible", time.Second, 5*time.Second, hostedEligibilityRecorded)
		blocker := block(440071)
		defer blocker.Rollback()
		before := snapshot()
		append := beginApply(observation(a, 4, "suspended", time.Second, 5*time.Second))
		waitHostedBarcodeSQL(t, ctx, db, "INSERT INTO gomar.discovery_scope_epoch")
		write := beginRequest("PUT", a.native(), directBody(2, "RACE-A"))
		waitHostedBarcodeSQL(t, ctx, db, "SELECT business_ref FROM gomar.discovery_paid_scope WHERE business_ref=$1 FOR SHARE")
		finish(blocker)
		finishApply(append)
		finishDenied(write)
		if snapshot() != before {
			t.Fatal("writer crossed committed suspension")
		}
	})
	t.Run("scope_holder_expiry_rolls_back_product_and_terminal_receipt", func(t *testing.T) {
		move(2 * time.Second)
		apply(a, 5, "eligible", 2*time.Second, 3*time.Second, hostedEligibilityRecorded)
		blocker := block(440072)
		defer blocker.Rollback()
		before := snapshot()
		approval := beginRequest("POST", aTarget+"/decision", aDecision)
		waitHostedBarcodeSQL(t, ctx, db, "UPDATE mcp_pos_catalog_proposals SET status=")
		append := beginApply(observation(a, 6, "suspended", 2100*time.Millisecond, 6*time.Second))
		waitHostedBarcodeSQL(t, ctx, db, "SELECT branches=$2::text[] FROM gomar.discovery_paid_scope")
		move(3100 * time.Millisecond)
		finish(blocker)
		finishDenied(approval)
		finishApply(append)
		if snapshot() != before {
			t.Fatal("expiry left a partial barcode, revision, proposal receipt, audit, sale or stock change")
		}
	})
	t.Run("same_actor_entity_audit_does_not_deadlock", func(t *testing.T) {
		exec(`INSERT INTO gomar.users(id,username,password,active,staff,superuser)
			VALUES (9001,'synthetic-barcode-audit-target','fixture',TRUE,FALSE,FALSE)`)
		product := barcodeTestScope{b.entity, b.outlet, 722, b.paid, b.branch}
		paused := block(440073)
		defer paused.Rollback()
		membership := beginRequest("POST", "/api/v1/entities/702/memberships", `{"usuario":"synthetic-barcode-audit-target"}`)
		waitHostedBarcodeSQL(t, ctx, db, "INSERT INTO access_events")
		barcode := beginRequest("PUT", product.native(), directBody(1, "QA-AUDIT"))
		waitHostedBarcodeSQL(t, ctx, db, "SELECT id FROM gomar.entities WHERE id=$1 FOR UPDATE")
		finish(paused)
		want := func(ch <-chan result, status int) {
			t.Helper()
			select {
			case got := <-ch:
				if got.err != nil || got.status != status {
					t.Fatalf("overlap returned %d, want %d: %s (%v)", got.status, status, got.body, got.err)
				}
			case <-ctx.Done():
				t.Fatal("same-actor audit and barcode did not both finish")
			}
		}
		want(membership, 201)
		want(barcode, 200)
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM gomar.access_events a JOIN gomar.memberships m ON m.id=a.membership_id
			WHERE a.actor_id=$1 AND a.entity_id=702 AND a.action='create_membership' AND m.user_id=9001
			AND m.active AND m.role='unassigned'`, actor).Scan(&count); err != nil || count != 1 || revision(product, "QA-AUDIT") != 2 {
			t.Fatal("same-actor overlap did not preserve both exact allowed results")
		}
	})
	if os.Getenv("GOMAR_QA_HOSTED_BARCODE_BROWSER") != "" {
		// Let fresh browser sessions issue within the unchanged per-actor
		// quotas. The API fixture's original login is no longer live.
		ask(f, c, "POST", "/api/v1/auth/logout", "", "", 200)
	}
	testHostedScopedChrome(t, ctx, f, control, "GOMAR_QA_HOSTED_BARCODE_BROWSER", "GOMAR_QA_HOSTED_BARCODE_MCP", "PASS: actual Chrome and packaged MCP hosted barcode", func(phase string) {
		switch phase {
		case "begin":
			move(5 * time.Second)
			apply(a, 20, "eligible", 4*time.Second, 8*time.Second, hostedEligibilityRecorded)
			apply(b, 20, "eligible", 4*time.Second, 9*time.Second, hostedEligibilityRecorded)
		case "suspend":
			before := snapshot()
			apply(a, 21, "suspended", 5*time.Second, 9*time.Second, hostedEligibilityRecorded)
			if snapshot() != before {
				t.Fatal("applying suspension changed operational data")
			}
		case "expire":
			// Browser signals this only after checking all stale A actions.
			if revision(a, "000801") != 3 {
				t.Fatal("denied browser flow changed A's existing barcode")
			}
			move(6 * time.Second)
			apply(a, 22, "eligible", 6*time.Second, 7*time.Second, hostedEligibilityRecorded)
			move(7100 * time.Millisecond)
		}
	})
	if os.Getenv("GOMAR_QA_HOSTED_BARCODE_BROWSER") != "" {
		if revision(a, "000801") != 3 || revision(b, "") != 5 || revision(self, "") != 5 {
			t.Fatal("combined browser test changed the wrong barcode or revision")
		}
		var pending bool
		if err := db.QueryRowContext(ctx, `SELECT status='pending' AND decided_at IS NULL AND change_preview IS NOT NULL
			FROM gomar.mcp_pos_catalog_proposals WHERE request_id='86000000-0000-4000-8000-000000000101'`).Scan(&pending); err != nil || !pending {
			t.Fatal("denied stale browser approval changed its pending receipt")
		}
		var unchanged bool
		if err := db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM gomar.pos_sales)
			AND NOT EXISTS(SELECT 1 FROM gomar.stock_movements) AND (SELECT count(*) FROM gomar.access_events)=1`).Scan(&unchanged); err != nil || !unchanged {
			t.Fatal("barcode browser proof affected unrelated tables")
		}
	}
}

func barcodeFixtureRequest(ctx context.Context, server discoveryTestServer, client *http.Client, method, path, body, bearer string) (int, []byte, error) {
	r, err := http.NewRequestWithContext(ctx, method, server.server.URL+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if method != "GET" {
		r.Header.Set("Origin", server.server.URL)
		r.Header.Set("Content-Type", "application/json")
		if client.Jar != nil {
			p, _ := url.Parse(server.server.URL)
			for _, k := range client.Jar.Cookies(p) {
				if k.Name == "csrftoken" {
					r.Header.Set("X-CSRFToken", k.Value)
				}
			}
		}
	}
	res, err := client.Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<10))
	if err == nil && res.Header.Get("Cache-Control") != "no-store" {
		err = fmt.Errorf("missing no-store")
	}
	return res.StatusCode, data, err
}

func waitHostedBarcodeSQL(t *testing.T, ctx context.Context, db *sql.DB, fragment string) {
	t.Helper()
	limit, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	step := time.NewTicker(8 * time.Millisecond)
	defer step.Stop()
	for {
		var waiting bool
		if err := db.QueryRowContext(limit, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
		AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND strpos(query,$1)>0)`, fragment).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-limit.Done():
			t.Fatalf("real PostgreSQL lock not observed: %s", fragment)
		case <-step.C:
		}
	}
}
