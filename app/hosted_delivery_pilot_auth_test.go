package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Reuse the ordinary setup listener's already-issued fixture identities; no
// login, credential issue, or other write route is exposed in this listener.
func exerciseDeliveryPilotRequestAuth(t *testing.T, ctx context.Context, a *app, origin string, owner, employee *http.Client, ownerKey string) {
	t.Helper()
	u, err := url.Parse(origin)
	if err != nil {
		t.Fatal(err)
	}
	session := func(cookies []*http.Cookie) []*http.Cookie {
		for _, c := range cookies {
			if c.Name == "sessionid" {
				return []*http.Cookie{c}
			}
		}
		t.Fatal("missing synthetic session")
		return nil
	}
	provider, worker := session(owner.Jar.Cookies(u)), session(employee.Jar.Cookies(u))
	// Give a separate synthetic classic account the same scoped read. Issue
	// its cookie and token outside the restricted listener.
	who := user{password: "fixture-classic-password-marker"}
	if err := a.db.QueryRowContext(ctx, `INSERT INTO users(username,password,active,staff,superuser)
		VALUES ('fixture-classic-delivery', $1, TRUE,FALSE,FALSE) RETURNING id`, who.password).Scan(&who.id); err != nil {
		t.Fatal(err)
	}
	var membership int64
	if err := a.db.QueryRowContext(ctx, `INSERT INTO memberships(user_id,entity_id,role,active)
		VALUES ($1,701,'read_only',TRUE) RETURNING id`, who.id).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `INSERT INTO outlet_grants VALUES ($1,711)`, membership); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := a.startSession(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, origin, nil), who); err != nil {
		t.Fatal(err)
	}
	classic := session(rec.Result().Cookies())
	tokenResponse := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, origin, strings.NewReader(`{"entity_id":701,"outlet_id":711}`))
	if err := a.issueMCPToken(tokenResponse, req, who); err != nil {
		t.Fatal(err)
	}
	var classicKey struct{ Token string }
	if json.Unmarshal(tokenResponse.Body.Bytes(), &classicKey) != nil || !providerRandom(classicKey.Token) {
		t.Fatal("could not issue scoped synthetic classic bearer")
	}
	for _, realm := range []struct {
		name                          string
		providerOnly, managed, hybrid bool
		classicStatus, workerStatus   int
	}{
		{"default", false, false, false, 200, 401},
		{"managed employee", false, true, false, 200, 200},
		{"provider only", true, false, false, 401, 401},
		{"provider and managed employee", false, false, true, 401, 200},
	} {
		t.Run(realm.name, func(t *testing.T) {
			// New listener per realm to keep live requests immutable. The
			// production startup validation is not part of this fixture.
			fixture := &app{db: a.db, hostedDiscoveryRead: a.hostedDiscoveryRead, enableDelivery: true, enableMCP: true,
				providerOnly: realm.providerOnly, employeeOnboarding: realm.managed, providerEmployee: realm.hybrid}
			s := newDeliveryPilotTestServer(t, fixture)
			const inboxA = "/api/v1/entities/701/outlets/711/delivery/orders"
			const inboxB = "/api/v1/entities/702/outlets/721/delivery/orders"
			for _, lane := range []struct {
				cookie []*http.Cookie
				want   int
			}{{provider, 200}, {classic, realm.classicStatus}, {worker, realm.workerStatus}} {
				got := pilotHTTPCall(t, ctx, s, inboxA, http.MethodGet, lane.cookie, "", lane.want)
				if bytes.Contains(got, []byte("qa-order-8002")) != (lane.want == 200) {
					t.Fatal("delivery leaked across an authentication realm or lost a valid session")
				}
			}
			crossStatus := http.StatusUnauthorized
			if realm.workerStatus == http.StatusOK {
				crossStatus = http.StatusNotFound
			}
			pilotHTTPCall(t, ctx, s, inboxB, http.MethodGet, worker, "", crossStatus)
			sdk := newDeliveryPilotSDKSession(t, ctx, s.URL, ownerKey)
			pilotSDKCall(t, ctx, sdk, "gomar_list_recent_delivery",
				map[string]any{"entity_id": int64(701), "outlet_id": int64(711)}, true)
			classicSDK := newDeliveryPilotSDKSession(t, ctx, s.URL, classicKey.Token)
			pilotSDKCall(t, ctx, classicSDK, "gomar_list_recent_delivery",
				map[string]any{"entity_id": int64(701), "outlet_id": int64(711)}, !realm.hybrid)
			pilotHTTPCall(t, ctx, s, "/api/v1/auth/employee/login", http.MethodPost, nil, "", 404)
		})
	}
}
