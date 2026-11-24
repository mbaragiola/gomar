package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureDiscoverySignIn = "/__discovery-fixture-sign-in"

type discoveryTestServer struct {
	t      *testing.T
	ctx    context.Context
	app    *app
	server *httptest.Server
}

func newDiscoveryTestServer(t *testing.T, ctx context.Context, db *sql.DB, mode *hostedDiscoveryReadFixture) discoveryTestServer {
	t.Helper()
	return newDiscoveryTestServerForSubject(t, ctx, db, mode, "fixture-hosted-discovery-user")
}

func newDiscoveryTestServerForSubject(t *testing.T, ctx context.Context, db *sql.DB, mode *hostedDiscoveryReadFixture, subject string) discoveryTestServer {
	t.Helper()
	var routes http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { routes.ServeHTTP(w, r) }))
	t.Cleanup(server.Close)
	host := server.Listener.Addr().String()
	a := &app{db: db, host: host, enableMCP: true, hostedDiscoveryRead: mode,
		enablePOS: true, enableMCPProposalSessions: true,
		providerAuth: &hostedProviderAuth{host: host, origin: "https://" + host}}
	mux := http.NewServeMux()
	mux.Handle("/", a.routes())
	mux.HandleFunc("GET "+fixtureDiscoverySignIn, func(w http.ResponseWriter, r *http.Request) {
		if !a.nativeSignupLocal(r, fixtureDiscoverySignIn) {
			http.NotFound(w, r)
			return
		}
		if err := a.startVerifiedProviderSession(w, r, verifiedProviderIdentity{"google", "https://accounts.google.com", subject}); err != nil {
			failure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	routes = mux
	server.StartTLS()
	return discoveryTestServer{t, ctx, a, server}
}

func (f discoveryTestServer) client() *http.Client {
	f.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Client{Transport: f.server.Client().Transport, Jar: jar}
}

func (f discoveryTestServer) request(c *http.Client, method, path, body, bearer string, status int) []byte {
	f.t.Helper()
	r, err := http.NewRequestWithContext(f.ctx, method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if method == http.MethodPost {
		r.Header.Set("Origin", f.server.URL)
		r.Header.Set("Content-Type", "application/json")
		p, _ := url.Parse(f.server.URL)
		for _, cookie := range c.Jar.Cookies(p) {
			if cookie.Name == "csrftoken" {
				r.Header.Set("X-CSRFToken", cookie.Value)
			}
		}
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := c.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	if err != nil || response.StatusCode != status {
		f.t.Fatalf("isolated discovery %s %s: %d want %d; %s (%v)", method, path, response.StatusCode, status, result, err)
	}
	return result
}

func (f discoveryTestServer) get(c *http.Client, path, bearer string, status int) []byte {
	f.t.Helper()
	out := f.request(c, http.MethodGet, path, "", bearer, status)
	for _, private := range []string{"paid-alpha", "paid-beta", "paid-ungranted", "scope-branch", "valid_until", "verified_at"} {
		if bytes.Contains(out, []byte(private)) {
			f.t.Fatal("private paid binding appeared in discovery response")
		}
	}
	return out
}

func TestPostgresHostedDiscoveryRead(t *testing.T) {
	ctx, db := testPostgresDB(t)
	const previous = 43
	if len(postgresMigrationNames) < previous+1 || postgresMigrationNames[previous-1] != "043_native_signup_plans.sql" ||
		postgresMigrationNames[previous] != "044_hosted_discovery_read.sql" {
		t.Fatal("044 must follow 043")
	}
	if err := migratePostgresThrough(ctx, db, postgresMigrationNames[:previous]); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO gomar.entities(id,name,cuit,active) VALUES
		(701,'QA A descubierto','00000000701',true),(702,'QA B descubierto','00000000702',true),
		(703,'QA propio','00000000703',true),(704,'QA sin clase','00000000704',true),
		(705,'QA sin permiso mapeado','00000000705',true);
		INSERT INTO gomar.outlets(id,entity_id,name,active) VALUES
		(711,701,'QA local A',true),(712,701,'QA A sin mapa',true),(721,702,'QA local B',true),
		(731,703,'QA local propio',true),(741,704,'QA local sin clase',true),
		(751,705,'QA mapa sin permiso',true),(752,705,'QA otro local sin mapa',true)`)
	if err := migratePostgres(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := verifyPostgresMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var noBackfill bool
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.entities)=5 AND (SELECT count(*) FROM gomar.outlets)=7
		AND NOT EXISTS(SELECT 1 FROM gomar.discovery_entity_class)
		AND NOT EXISTS(SELECT 1 FROM gomar.discovery_paid_scope)
		AND NOT EXISTS(SELECT 1 FROM gomar.discovery_paid_outlet)
		AND NOT EXISTS(SELECT 1 FROM gomar.discovery_scope_epoch)`).Scan(&noBackfill); err != nil || !noBackfill {
		t.Fatal("044 changed or classified existing entities")
	}
	var now atomic.Int64
	start := time.Now().UTC().Truncate(time.Microsecond)
	now.Store(start.UnixMicro())
	f := newDiscoveryTestServer(t, ctx, db, &hostedDiscoveryReadFixture{now: func() time.Time { return time.UnixMicro(now.Load()) }})
	base := newDiscoveryTestServer(t, ctx, db, nil)
	c, control := f.client(), base.client()
	f.get(c, fixtureDiscoverySignIn, "", 204)
	base.get(control, fixtureDiscoverySignIn, "", 204)
	var actor int64
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM gomar.provider_identities WHERE subject='fixture-hosted-discovery-user'`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
		SELECT id,$1,id,'read_only',true FROM gomar.entities WHERE id BETWEEN 701 AND 705`, actor)
	exec(`INSERT INTO gomar.outlet_grants VALUES(701,711),(701,712),(702,721),(703,731),(704,741),(705,752)`)
	exec(`INSERT INTO gomar.discovery_entity_class VALUES
		(701,'paid_hosted'),(702,'paid_hosted'),(703,'self_host'),(705,'paid_hosted');
		INSERT INTO gomar.discovery_paid_scope VALUES
		('paid-alpha',ARRAY['scope-branch-a']),('paid-beta',ARRAY['scope-branch-b']),
		('paid-ungranted',ARRAY['scope-branch-u']);
		INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref) VALUES
		(711,701,'paid-alpha','scope-branch-a'),(721,702,'paid-beta','scope-branch-b'),
		(751,705,'paid-ungranted','scope-branch-u')`)
	reject := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err == nil {
			t.Fatal("invalid binding or history mutation was accepted")
		}
	}
	reject(`INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref)
		VALUES(712,702,'paid-beta','false-owner')`)
	reject(`INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref)
		VALUES(712,701,'paid-alpha','scope-branch-a')`)
	reject(`INSERT INTO gomar.discovery_paid_outlet(outlet_id,entity_id,business_ref,branch_ref)
		VALUES(731,703,'paid-alpha','self-claimed')`)
	reject(`UPDATE gomar.discovery_entity_class SET class='self_host' WHERE entity_id=701`)
	reject(`INSERT INTO gomar.discovery_scope_epoch VALUES('paid-alpha',ARRAY['wrong-outlet'],1,'eligible',$1,$2)`,
		start, start.Add(time.Second))
	reject(`INSERT INTO gomar.discovery_scope_epoch VALUES('paid-alpha',ARRAY['scope-branch-a'],1,'eligible',$1,$2)`,
		start, start.Add(10*time.Second))
	epoch := func(scope string, version int64, state string, checked, expires time.Time) {
		t.Helper()
		exec(`INSERT INTO gomar.discovery_scope_epoch
			SELECT business_ref,branches,$2,$3,$4,$5 FROM gomar.discovery_paid_scope WHERE business_ref=$1`,
			scope, version, state, checked, expires)
	}

	entitiesPath := "/api/v1/entities"
	aOutlets := entitiesPath + "/701/outlets"
	mcpEntitiesPath := "/api/v1/mcp/entities"
	checkIDs := func(raw []byte, want ...int64) {
		t.Helper()
		type item struct {
			ID int64 `json:"id"`
		}
		var got []item
		if json.Unmarshal(raw, &got) != nil {
			t.Fatal("invalid discovery result")
		}
		ids := make([]int64, 0, len(got))
		for _, x := range got {
			ids = append(ids, x.ID)
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("discovery IDs %v, want %v", ids, want)
		}
	}
	checkMCP := func(path, token, field string, want ...int64) {
		t.Helper()
		var result map[string]json.RawMessage
		raw := f.get(c, path, token, 200)
		if json.Unmarshal(raw, &result) != nil || len(result) != 1 {
			t.Fatal("invalid MCP discovery result")
		}
		checkIDs(result[field], want...)
	}
	deadline := start.Add(4 * time.Second)
	checkIDs(f.get(c, entitiesPath, "", 200), 703) // No eligibility: explicit self-host only.
	epoch("paid-alpha", 1, "eligible", start.Add(-time.Second), deadline)
	epoch("paid-beta", 1, "eligible", start.Add(-time.Second), deadline)
	epoch("paid-ungranted", 1, "eligible", start.Add(-time.Second), deadline)
	reject(`UPDATE gomar.discovery_scope_epoch SET state='unknown' WHERE business_ref='paid-alpha'`)
	reject(`DELETE FROM gomar.discovery_scope_epoch WHERE business_ref='paid-alpha'`)
	checkIDs(f.get(c, entitiesPath, "", 200), 701, 702, 703)
	checkIDs(f.get(c, aOutlets, "", 200), 711)
	checkIDs(base.get(control, entitiesPath, "", 200), 701, 702, 703, 704, 705)
	checkIDs(base.get(control, aOutlets, "", 200), 712, 711)

	issue := func(body string) string {
		t.Helper()
		var got struct{ Token string }
		if json.Unmarshal(f.request(c, http.MethodPost, "/api/v1/auth/mcp-token", body, "", 201), &got) != nil || !providerRandom(got.Token) {
			t.Fatal("could not issue exact synthetic read token")
		}
		return got.Token
	}
	aToken := issue(`{"entity_id":701,"outlet_id":711}`)
	bToken := issue(`{"entity_id":702,"outlet_id":721}`)
	unmappedToken := issue(`{"entity_id":701,"outlet_id":712}`)
	checkMCP(mcpEntitiesPath, aToken, "entities", 701)
	checkMCP(mcpEntitiesPath+"/701/outlets", aToken, "outlets", 711)
	f.get(c, mcpEntitiesPath, unmappedToken, 404)
	f.get(c, mcpEntitiesPath+"/702/outlets", aToken, 404)

	counts := func() (n [7]int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM gomar.entities),(SELECT count(*) FROM gomar.outlets),
			(SELECT count(*) FROM gomar.memberships),(SELECT count(*) FROM gomar.outlet_grants),
			(SELECT count(*) FROM gomar.access_events),(SELECT count(*) FROM gomar.mcp_read_tokens),
			(SELECT count(*) FROM gomar.mcp_proposal_sessions)`).
			Scan(&n[0], &n[1], &n[2], &n[3], &n[4], &n[5], &n[6]); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := counts()
	deniedA := func() {
		t.Helper()
		raw := f.get(c, entitiesPath, "", 200)
		checkIDs(raw, 702, 703)
		if bytes.Contains(raw, []byte("QA A descubierto")) || bytes.Contains(raw, []byte("00000000701")) {
			t.Fatal("suspended legal entity name or CUIT disclosed")
		}
		f.get(c, aOutlets, "", 404)
		f.get(c, mcpEntitiesPath, aToken, 404)
		f.get(c, mcpEntitiesPath+"/701/outlets", aToken, 404)
		checkMCP(mcpEntitiesPath, bToken, "entities", 702)
		checkMCP(mcpEntitiesPath+"/702/outlets", bToken, "outlets", 721)
		checkIDs(f.get(c, entitiesPath+"/703/outlets", "", 200), 731)
	}
	epoch("paid-alpha", 3, "suspended", start, deadline)
	epoch("paid-alpha", 2, "eligible", start.Add(-500*time.Millisecond), deadline)
	deniedA()
	epoch("paid-alpha", 4, "eligible", start.Add(-time.Millisecond), deadline)
	deniedA()
	epoch("paid-alpha", 5, "eligible", start.Add(100*time.Millisecond), deadline)
	deniedA() // Future evidence cannot be used.
	now.Store(start.Add(200 * time.Millisecond).UnixMicro())
	checkIDs(f.get(c, entitiesPath, "", 200), 701, 702, 703)
	epoch("paid-alpha", 6, "eligible", start.Add(200*time.Millisecond), start.Add(time.Second))
	now.Store(start.Add(1200 * time.Millisecond).UnixMicro())
	deniedA() // A newer expired winner never falls back to live epoch 5.
	epoch("paid-alpha", 7, "unknown", start.Add(time.Second), deadline)
	epoch("paid-alpha", 7, "eligible", start.Add(1100*time.Millisecond), deadline)
	deniedA() // Evidence disputes at the highest epoch fail closed.
	checkIDs(base.get(control, entitiesPath, "", 200), 701, 702, 703, 704, 705)
	var legacyMCP map[string]json.RawMessage
	if json.Unmarshal(base.get(control, mcpEntitiesPath, aToken, 200), &legacyMCP) != nil {
		t.Fatal("invalid ungated control MCP read")
	}
	checkIDs(legacyMCP["entities"], 701)
	checkMCP(mcpEntitiesPath, bToken, "entities", 702)
	if counts() != before {
		t.Fatal("discovery performed writes or changed grants")
	}

	testHostedScopedChrome(t, ctx, f, base, "GOMAR_QA_DISCOVERY_BROWSER", "GOMAR_QA_DISCOVERY_MCP", "PASS: actual Chrome and packaged MCP hosted discovery", func(step string) {
		switch step {
		case "begin":
			now.Store(start.Add(3 * time.Second).UnixMicro())
			epoch("paid-alpha", 20, "eligible", start.Add(2*time.Second), start.Add(6*time.Second))
			epoch("paid-beta", 20, "eligible", start.Add(2*time.Second), start.Add(7*time.Second))
		case "suspend":
			epoch("paid-alpha", 21, "suspended", start.Add(3*time.Second), start.Add(7*time.Second))
			epoch("paid-alpha", 19, "eligible", start.Add(2*time.Second), start.Add(6*time.Second))
		case "expire":
			epoch("paid-alpha", 22, "eligible", start.Add(4*time.Second), start.Add(5*time.Second))
			now.Store(start.Add(5500 * time.Millisecond).UnixMicro())
		}
	})
	if os.Getenv("GOMAR_QA_DISCOVERY_BROWSER") != "" {
		before[5] += 2 // Only fresh synthetic A/B credentials issued by the fixture.
	}
	if counts() != before {
		t.Fatal("the browser or packaged discovery changed scoped catalog/grant data")
	}
	t.Run("self_host_native_projection_parity", func(t *testing.T) {
		// Run the same ordinary principal against the opted-in fixture and
		// the default composition, with live paid evidence and both grants.
		now.Store(start.Add(10 * time.Second).UnixMicro())
		epoch("paid-alpha", 30, "eligible", start.Add(9*time.Second), start.Add(13*time.Second))
		exec(`UPDATE gomar.memberships SET role='unassigned' WHERE id IN(701,703)`)
		readItem := func(server discoveryTestServer, client *http.Client, id int64) (json.RawMessage, []string) {
			t.Helper()
			var list []json.RawMessage
			if err := json.Unmarshal(server.get(client, entitiesPath, "", 200), &list); err != nil {
				t.Fatal(err)
			}
			for _, raw := range list {
				var item struct {
					ID   int64    `json:"id"`
					Caps []string `json:"capacidades"`
				}
				if err := json.Unmarshal(raw, &item); err != nil {
					t.Fatal(err)
				}
				if item.ID == id {
					return raw, item.Caps
				}
			}
			return nil, nil
		}
		for _, id := range []int64{701, 703} {
			s, _ := readItem(f, c, id)
			d, _ := readItem(base, control, id)
			if s != nil || d != nil {
				t.Fatal("bare unassigned membership should not acquire native entity visibility")
			}
		}
		// Establish genuine previously issued credentials, then drop the
		// read role while those records and the ordinary sessions remain.
		exec(`UPDATE gomar.memberships SET role='administration' WHERE id IN(701,703)`)
		cToken := issue(`{"entity_id":703,"outlet_id":731}`)
		for _, req := range []string{
			`{"entity_id":701,"outlet_id":711,"actions":["pos_product_rename"]}`,
			`{"entity_id":703,"outlet_id":731,"actions":["pos_product_rename"]}`,
		} {
			f.request(c, http.MethodPost, proposalSessionsAuthPath, req, "", 201)
		}
		exec(`UPDATE gomar.memberships SET role='unassigned' WHERE id IN(701,703)`)
		before := counts()
		selfHost, caps := readItem(f, c, 703)
		legacy, _ := readItem(base, control, 703)
		if !bytes.Equal(selfHost, legacy) || !slices.Equal(caps, []string{"mcp_proposal_session_management"}) {
			t.Fatal("explicit self-host lost or widened the original proposal-session-only projection")
		}
		paid, _ := readItem(f, c, 701)
		_, legacyCaps := readItem(base, control, 701)
		if paid != nil || !slices.Equal(legacyCaps, []string{"mcp_proposal_session_management"}) {
			t.Fatal("proposal-only paid membership must not reveal entity metadata")
		}
		for _, lane := range []struct {
			server discoveryTestServer
			client *http.Client
		}{{f, c}, {base, control}} {
			lane.server.get(lane.client, aOutlets, "", 403)
			lane.server.get(lane.client, mcpEntitiesPath, aToken, 403)
			lane.server.get(lane.client, entitiesPath+"/703/outlets", "", 403)
			lane.server.get(lane.client, mcpEntitiesPath, cToken, 403)
			lane.server.get(lane.client, mcpEntitiesPath+"/703/outlets", cToken, 403)
		}
		if counts() != before {
			t.Fatal("self-host parity changed discovery or credential state")
		}
	})
	t.Log("hosted discovery only: exact grants and maps, suspension/expiry/conflict deny A; B and self-host remain")
}

func testHostedScopedChrome(t *testing.T, ctx context.Context, f, base discoveryTestServer, scriptEnv, binaryEnv, passMarker string, move func(string)) {
	t.Helper()
	script := os.Getenv(scriptEnv)
	if script == "" {
		return
	}
	mcp, python := os.Getenv(binaryEnv), os.Getenv("GOMAR_QA_SIGNUP_PYTHON")
	if !filepath.IsAbs(script) || !filepath.IsAbs(mcp) || !filepath.IsAbs(python) {
		t.Fatal("require absolute isolated browser and MCP commands")
	}
	scratch := t.TempDir()
	cert := filepath.Join(scratch, "fixture.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	move("begin")
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(ctx, python, "-B", script, f.server.URL, base.server.URL, mcp, cert, scratch)
	type runResult struct {
		body []byte
		err  error
	}
	done := make(chan runResult, 1)
	go func() { body, err := cmd.CombinedOutput(); done <- runResult{body, err} }()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for _, phase := range []string{"suspend", "expire"} {
		for {
			select {
			case result := <-done:
				t.Fatalf("Chrome ended before phase %s: %v\n%s", phase, result.err, result.body)
			case <-ctx.Done():
				t.Fatal("isolated Chrome did not signal the fixture phase")
			case <-tick.C:
			}
			if _, err := os.Stat(filepath.Join(scratch, "ready-"+phase)); os.IsNotExist(err) {
				continue
			} else if err != nil {
				t.Fatal(err)
			}
			move(phase)
			if err := os.WriteFile(filepath.Join(scratch, "observed-"+phase), nil, 0600); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	result := <-done
	if result.err != nil || !bytes.Contains(result.body, []byte(passMarker)) {
		t.Fatalf("isolated scoped-read QA failed: %v\n%s", result.err, result.body)
	}
	t.Log(string(result.body))
}
