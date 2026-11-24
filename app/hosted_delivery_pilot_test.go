package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/mbaragiola/gomar/app/internal/gomarmcp"
	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This isolated listener has no fallback to app.routes, shell, assets, or
// operational write routes. Synthetic sessions and bearer keys are issued out
// of band. It uses the app's request-level authentication policy; login
// and session upkeep remain the responsibility of the existing handlers.
// Loopback HTTP uses the unchanged Go MCP client.
func newDeliveryPilotTestServer(t *testing.T, a *app) *httptest.Server {
	t.Helper()
	if a.hostedDiscoveryRead == nil {
		t.Fatal("pilot fixture requires hosted scope checks")
	}
	mux := http.NewServeMux()
	bind := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }
	bind("/api/v1/entities", a.protected(a.entities))
	bind("/api/v1/entities/{entity}/outlets", a.scoped("read_business_data", a.listDiscoveryOutlets))
	want := map[string]bool{
		"/api/v1/entities/{entity}/outlets/{outlet}/delivery/orders": true,
		"/api/v1/mcp/entities":                                                   true,
		"/api/v1/mcp/entities/{entity}/outlets":                                  true,
		"/api/v1/mcp/entities/{entity}/outlets/{outlet}/delivery-orders":         true,
		"/api/v1/mcp/entities/{entity}/outlets/{outlet}/delivery-status-summary": true,
	}
	keep := func(pattern, method string, h http.HandlerFunc) {
		if method == http.MethodGet && want[pattern] {
			bind(pattern, h)
			delete(want, pattern)
		}
	}
	a.registerDelivery(keep)
	a.registerMCP(keep)
	if len(want) != 0 {
		t.Fatalf("pilot fixture is missing registrations: %v", want)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Do not implicitly admit HEAD, arbitrary queries or cleaned redirects.
		if r.Method != http.MethodGet || r.URL.RawPath != "" || r.URL.EscapedPath() != r.URL.Path ||
			r.URL.RawQuery != "" || r.URL.ForceQuery || path.Clean(r.URL.Path) != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, a.requestAuthPolicy(r))
	}))
	t.Cleanup(s.Close)
	return s
}

var deliveryPilotToolNames = []string{
	"gomar_delivery_status_summary", "gomar_list_entities", "gomar_list_outlets", "gomar_list_recent_delivery",
}

// The in-memory connection exercises the Go SDK's actual list and call
// dispatch with the same tool registrations as the packaged program.
func newDeliveryPilotSDKSession(t *testing.T, ctx context.Context, origin, token string) *mcp.ClientSession {
	t.Helper()
	backend, err := mcpclient.New(origin, token)
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "isolated-delivery-test", Version: "0"}, nil)
	gomarmcp.AddDiscoveryTools(server, backend)
	gomarmcp.AddDeliveryTools(server, backend)
	serverWire, clientWire := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "isolated-test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientWire, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	listed, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("pilot tool %q is not read-only", tool.Name)
		}
	}
	slices.Sort(names)
	if listed.NextCursor != "" || !slices.Equal(names, deliveryPilotToolNames) {
		t.Fatalf("pilot MCP catalog = %v, want %v", names, deliveryPilotToolNames)
	}
	return cs
}

func pilotSDKCall(t *testing.T, ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any, allowed bool) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || out == nil || out.IsError == allowed || (out.StructuredContent != nil) != allowed {
		t.Fatalf("pilot SDK %s allowed=%t: error=%v result=%v", name, allowed, err, out)
	}
	js, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"qa-order", "QA producto", "qa-store", "scope-branch", "paid-alpha", "paid-beta"} {
		if bytes.Contains(js, []byte(private)) {
			t.Fatalf("private marker %q in MCP result %s", private, name)
		}
	}
	if !allowed {
		if bytes.Contains(js, []byte("8002")) || bytes.Contains(js, []byte("8201")) {
			t.Fatalf("denied MCP result %s retained order data", name)
		}
		return
	}
	body, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	switch name {
	case "gomar_list_entities":
		var entities mcpclient.Entities
		if json.Unmarshal(body, &entities) != nil || len(entities.Entities) != 1 ||
			(entities.Entities[0].ID != 701 && entities.Entities[0].ID != 702) {
			t.Fatal("MCP discovery result did not contain one scoped entity")
		}
	case "gomar_list_outlets":
		var outlets mcpclient.Outlets
		want := map[int64][]int64{701: {711}, 702: {721, 722}}[args["entity_id"].(int64)]
		if json.Unmarshal(body, &outlets) != nil || len(outlets.Outlets) != len(want) {
			t.Fatal("MCP discovery result did not contain scoped outlets")
		}
		for i, outlet := range outlets.Outlets {
			if outlet.ID != want[i] {
				t.Fatal("MCP discovery returned an incorrect outlet")
			}
		}
	case "gomar_list_recent_delivery":
		var delivery mcpclient.Delivery
		want := map[int64]int64{711: 8002, 721: 8201}[args["outlet_id"].(int64)]
		if json.Unmarshal(body, &delivery) != nil || len(delivery.Orders) == 0 || delivery.Orders[0].ID != want {
			t.Fatal("MCP delivery returned orders from the wrong outlet")
		}
	case "gomar_delivery_status_summary":
		var status mcpclient.DeliveryStatusSummary
		if json.Unmarshal(body, &status) != nil || status.WindowLimit != 100 {
			t.Fatal("MCP delivery did not return the expected status window")
		}
	}
}

func pilotHTTPCall(t *testing.T, ctx context.Context, s *httptest.Server, originPath, method string, cookies []*http.Cookie, bearer string, status int) []byte {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, method, s.URL+originPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	c := &http.Client{Transport: s.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	// Each expected response is far smaller than this limit.
	got, err := io.ReadAll(io.LimitReader(res.Body, 128<<10))
	if err != nil || res.StatusCode != status || res.Header.Get("Cache-Control") != "no-store" ||
		res.Header.Get("Location") != "" {
		t.Fatalf("pilot HTTP %s %s: got %d, want %d without a redirect/cache (%v)", method, originPath, res.StatusCode, status, err)
	}
	return got
}

func exerciseDeliveryPilot(t *testing.T, ctx context.Context, a *app, browser *http.Client, browserOrigin, keyA, keyB string) func(aEligible bool) {
	t.Helper()
	s := newDeliveryPilotTestServer(t, a)
	verifiedOrigin, err := url.Parse(browserOrigin)
	if err != nil {
		t.Fatal(err)
	}
	var cookies []*http.Cookie
	for _, c := range browser.Jar.Cookies(verifiedOrigin) {
		if c.Name == "sessionid" {
			cookies = append(cookies, c)
		}
	}
	if len(cookies) != 1 {
		t.Fatal("fixture must already have one browser session")
	}
	csA := newDeliveryPilotSDKSession(t, ctx, s.URL, keyA)
	csB := newDeliveryPilotSDKSession(t, ctx, s.URL, keyB)
	for _, name := range []string{"gomar_current_user", "gomar_payables_summary", "gomar_propose_pos_rename"} {
		out, err := csA.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		var rpc *jsonrpc.Error
		if out != nil || !errors.As(err, &rpc) || rpc.Code != jsonrpc.CodeInvalidParams || rpc.Message != fmt.Sprintf("unknown tool %q", name) {
			t.Fatalf("unlisted pilot tool %q did not reject the call as unknown: %v", name, err)
		}
	}
	httpRead := func(p string, status int) []byte {
		return pilotHTTPCall(t, ctx, s, p, http.MethodGet, cookies, "", status)
	}
	for _, op := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/static/app.js"}, {"GET", "/pos-sw.js"}, {"GET", "/admin/"},
		{"GET", "/api/v1/public/reservas/test"}, {"POST", "/api/v1/auth/mcp-token"},
		{"GET", "/api/v1/mcp/entities/701/outlets/711/payables-summary"},
		{"GET", "/api/v1/entities/701/outlets/711/knowledge"},
		{"POST", "/api/v1/entities/701/outlets/711/pos-barcode-proposals/1/decision"},
		{"HEAD", "/api/v1/entities"}, {"POST", "/api/v1/entities"}, {"GET", "/api/v1/entities/"},
		{"OPTIONS", "/api/v1/entities"}, {"GET", "/api/v1/entities?"},
	} {
		got := pilotHTTPCall(t, ctx, s, op.path, op.method, cookies, keyA, 404)
		if len(got) > 0 && strings.TrimSpace(string(got)) != "404 page not found" {
			t.Fatalf("outside pilot returned non-generic response for %v", op)
		}
	}
	pilotHTTPCall(t, ctx, s, "/api/v1/entities", http.MethodGet, nil, "", 401)
	pilotHTTPCall(t, ctx, s, "/api/v1/mcp/entities", http.MethodGet, nil, "", 401)
	pilotSDKCall(t, ctx, csA, "gomar_list_recent_delivery", map[string]any{"entity_id": 702, "outlet_id": 721}, false)
	return func(aEligible bool) {
		t.Helper()
		selection := httpRead("/api/v1/entities", 200)
		if bytes.Contains(selection, []byte("QA Delivery A")) != aEligible || !bytes.Contains(selection, []byte("QA Delivery B")) {
			t.Fatal("browser discovery did not reflect independent paid scopes")
		}
		for _, lane := range []struct {
			entity, outlet int64
			key            string
			session        *mcp.ClientSession
			allowed        bool
		}{{701, 711, keyA, csA, aEligible}, {702, 721, keyB, csB, true}} {
			e := fmt.Sprintf("/api/v1/entities/%d/outlets", lane.entity)
			inbox := fmt.Sprintf("%s/%d/delivery/orders", e, lane.outlet)
			code := 404
			if lane.allowed {
				code = 200
			}
			httpRead(e, code)
			got := httpRead(inbox, code)
			if bytes.Contains(got, []byte("qa-order-")) != lane.allowed {
				t.Fatal("browser inbox scope changed or returned data on denial")
			}
			pilotSDKCall(t, ctx, lane.session, "gomar_list_entities", nil, lane.allowed)
			pilotSDKCall(t, ctx, lane.session, "gomar_list_outlets", map[string]any{"entity_id": lane.entity}, lane.allowed)
			for _, name := range []string{"gomar_list_recent_delivery", "gomar_delivery_status_summary"} {
				pilotSDKCall(t, ctx, lane.session, name, map[string]any{"entity_id": lane.entity, "outlet_id": lane.outlet}, lane.allowed)
			}
			bridge := fmt.Sprintf("/api/v1/mcp/entities/%d/outlets/%d/delivery-orders", lane.entity, lane.outlet)
			pilotHTTPCall(t, ctx, s, bridge, http.MethodGet, nil, lane.key, code)
		}
	}
}
