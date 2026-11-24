package main

import (
	"database/sql"
	"net/http"
	"testing"
)

// Test-only planning ledger. A registration is not authorization and none of
// these entries is an allow rule in a running router.
type pilotRegistration struct{ method, pattern string }

type pilotStage uint8

const (
	pilotExcluded pilotStage = iota
	pilotBootstrapNeedsRestriction
	pilotBrowserBranchRead
	pilotMCPBranchRead
)

func deliveryPilotStage(r pilotRegistration) pilotStage {
	switch r {
	case pilotRegistration{"", "/healthz"},
		pilotRegistration{http.MethodGet, "/api/v1/auth/csrf"},
		pilotRegistration{http.MethodGet, "/api/v1/auth/me"},
		pilotRegistration{http.MethodPost, "/api/v1/auth/logout"}:
		return pilotBootstrapNeedsRestriction
	case pilotRegistration{http.MethodGet, "/api/v1/entities"},
		pilotRegistration{http.MethodGet, "/api/v1/entities/{entity}/outlets"},
		pilotRegistration{http.MethodGet, "/api/v1/entities/{entity}/outlets/{outlet}/delivery/orders"}:
		return pilotBrowserBranchRead
	case pilotRegistration{http.MethodGet, "/api/v1/mcp/entities"},
		pilotRegistration{http.MethodGet, "/api/v1/mcp/entities/{entity}/outlets"},
		pilotRegistration{http.MethodGet, "/api/v1/mcp/entities/{entity}/outlets/{outlet}/delivery-orders"},
		pilotRegistration{http.MethodGet, "/api/v1/mcp/entities/{entity}/outlets/{outlet}/delivery-status-summary"}:
		return pilotMCPBranchRead
	default:
		return pilotExcluded
	}
}

func TestHostedDeliveryPilotRegistrationInventory(t *testing.T) {
	const host = "127.0.0.1:9010"
	wide := func(delivery, mcp bool) *app {
		return &app{
			host: host,
			// Route composition only checks for a nonnil DB. No request is handled.
			db:                           &sql.DB{},
			enableDelivery:               delivery,
			enableMCP:                    mcp,
			enablePOS:                    true,
			enableStock:                  true,
			enableMCPPOSCatalogProposals: true,
			publicReservations:           true,
			employeeOnboarding:           true,
			nativeSignup: &nativeSignupHosted{
				Enabled: true, Origin: "https://" + host, Audience: "inventory-fixture",
			},
			providerAuth: &hostedProviderAuth{host: host, origin: "https://" + host},
		}
	}
	narrow := &app{host: host}
	type expectation struct {
		pilotRegistration
		stage  pilotStage
		narrow bool
	}
	expectations := []expectation{
		{pilotRegistration{"", "/healthz"}, pilotBootstrapNeedsRestriction, true},
		{pilotRegistration{"GET", "/api/v1/auth/csrf"}, pilotBootstrapNeedsRestriction, true},
		{pilotRegistration{"GET", "/api/v1/auth/me"}, pilotBootstrapNeedsRestriction, true},
		{pilotRegistration{"POST", "/api/v1/auth/logout"}, pilotBootstrapNeedsRestriction, true},
		{pilotRegistration{"GET", "/api/v1/entities"}, pilotBrowserBranchRead, true},
		{pilotRegistration{"GET", "/api/v1/entities/{entity}/outlets"}, pilotBrowserBranchRead, true},
		{pilotRegistration{"GET", "/api/v1/entities/{entity}/outlets/{outlet}/delivery/orders"}, pilotBrowserBranchRead, false},
		{pilotRegistration{"GET", "/api/v1/mcp/entities"}, pilotMCPBranchRead, false},
		{pilotRegistration{"GET", "/api/v1/mcp/entities/{entity}/outlets"}, pilotMCPBranchRead, false},
		{pilotRegistration{"GET", "/api/v1/mcp/entities/{entity}/outlets/{outlet}/delivery-orders"}, pilotMCPBranchRead, false},
		{pilotRegistration{"GET", "/api/v1/mcp/entities/{entity}/outlets/{outlet}/delivery-status-summary"}, pilotMCPBranchRead, false},
		{pilotRegistration{"", "/"}, pilotExcluded, true},
		{pilotRegistration{"", "/admin"}, pilotExcluded, true},
		{pilotRegistration{"", "/admin/"}, pilotExcluded, true},
		{pilotRegistration{"GET", "/pos"}, pilotExcluded, false},
		{pilotRegistration{"GET", "/pos-sw.js"}, pilotExcluded, false},
		{pilotRegistration{"GET", "/reservas/{venue}"}, pilotExcluded, false},
		{pilotRegistration{"GET", "/alta"}, pilotExcluded, false},
		{pilotRegistration{"POST", "/api/v1/alta/confirmar"}, pilotExcluded, false},
		{pilotRegistration{"POST", "/api/v1/auth/employee/password"}, pilotExcluded, false},
		{pilotRegistration{"POST", "/api/v1/auth/mcp-token"}, pilotExcluded, false},
		{pilotRegistration{"GET", "/api/v1/public/reservas/{venue}"}, pilotExcluded, true},
		{pilotRegistration{"POST", "/api/v1/entities/{entity}/outlets/{outlet}/knowledge"}, pilotExcluded, true},
		{pilotRegistration{"GET", "/api/v1/mcp/entities/{entity}/outlets/{outlet}/knowledge"}, pilotExcluded, false},
		{pilotRegistration{"POST", "/api/v1/mcp/entities/{entity}/outlets/{outlet}/pos-barcode-proposals"}, pilotExcluded, false},
		{pilotRegistration{"POST", "/api/v1/entities/{entity}/outlets/{outlet}/pos-barcode-proposals/{proposal}/decision"}, pilotExcluded, false},
	}
	for _, fixture := range []struct {
		name   string
		app    *app
		narrow bool
	}{
		{"default", narrow, true},
		{"feature-fixture", wide(true, true), false},
		{"delivery-off", wide(false, true), false},
		{"mcp-off", wide(true, false), false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			seen := make(map[pilotRegistration]bool)
			fixture.app.composeRoutes(func(method, pattern string) {
				r := pilotRegistration{method, pattern}
				if seen[r] {
					t.Errorf("duplicate route registration: %+v", r)
				}
				seen[r] = true
			})
			proposed := make(map[pilotRegistration]pilotStage)
			for _, want := range expectations {
				present := !fixture.narrow || want.narrow
				switch want.pattern {
				case "/api/v1/entities/{entity}/outlets/{outlet}/delivery/orders":
					present = fixture.app.enableDelivery
				case "/api/v1/mcp/entities/{entity}/outlets/{outlet}/delivery-orders",
					"/api/v1/mcp/entities/{entity}/outlets/{outlet}/delivery-status-summary":
					present = fixture.app.enableDelivery && fixture.app.enableMCP
				case "/api/v1/auth/mcp-token",
					"/api/v1/mcp/entities/{entity}/outlets/{outlet}/knowledge":
					present = fixture.app.enableMCP
				default:
					if want.stage == pilotMCPBranchRead {
						present = fixture.app.enableMCP
					}
				}
				if got := seen[want.pilotRegistration]; got != present {
					t.Errorf("registration %+v present = %t, want %t", want.pilotRegistration, got, present)
				}
				if got := deliveryPilotStage(want.pilotRegistration); got != want.stage {
					t.Errorf("registration %+v stage = %d, want %d", want.pilotRegistration, got, want.stage)
				}
				if want.stage != pilotExcluded {
					proposed[want.pilotRegistration] = want.stage
				}
			}
			for r := range seen {
				if got := deliveryPilotStage(r); got != proposed[r] {
					t.Errorf("unexpected pilot candidate: %+v, stage %d", r, got)
				}
			}
		})
	}
	for _, r := range []pilotRegistration{
		{"HEAD", "/healthz"},
		{"HEAD", "/api/v1/entities"},
		{"POST", "/api/v1/entities"},
		{"GET", "/api/v1/entities/{entity}/outlets/{outlet}/delivery/unreviewed"},
		{"GET", "/api/v1/mcp/entities/{entity}/outlets/{outlet}/payables-summary"},
		{"GET", "/api/v1/entities/1/outlets/2/delivery/orders"},
	} {
		if got := deliveryPilotStage(r); got != pilotExcluded {
			t.Errorf("non-exact or out-of-scope entry %+v was classified as %d", r, got)
		}
	}
}
