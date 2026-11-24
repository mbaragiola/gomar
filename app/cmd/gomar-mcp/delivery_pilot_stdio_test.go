package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/mbaragiola/gomar/app/internal/gomarmcp"
	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This entry point exists only in the test executable, not in gomar-mcp.
func TestMain(m *testing.M) {
	if os.Getenv("GOMAR_QA_DELIVERY_STDIO") != "1" {
		os.Exit(m.Run())
	}
	if err := serveDeliveryPilotFixture(); err != nil {
		fmt.Fprintln(os.Stderr, "delivery pilot fixture did not start or stop cleanly")
		os.Exit(1)
	}
}

func deliveryPilotFixtureOrigin(origin string) bool {
	host, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		return false
	}
	addr, err := netip.ParseAddrPort(host)
	return err == nil && addr.Port() >= 1024 && addr.String() == host &&
		addr.Addr() == netip.AddrFrom4([4]byte{127, 0, 0, 1})
}

func serveDeliveryPilotFixture() error {
	origin := os.Getenv("GOMAR_QA_DELIVERY_ORIGIN")
	if len(os.Args) != 1 || !deliveryPilotFixtureOrigin(origin) {
		return errors.New("expected one local fixture origin and no CLI arguments")
	}
	token, err := mcpclient.ReadToken("fixture.key")
	if err != nil {
		return err
	}
	backend, err := mcpclient.New(origin, token)
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "isolated-delivery-stdio", Version: "0"}, nil)
	gomarmcp.AddDiscoveryTools(server, backend)
	gomarmcp.AddDeliveryTools(server, backend)
	return server.Run(context.Background(), &mcp.StdioTransport{MaxLineLength: 16 * 1024})
}

func TestDeliveryPilotFixtureOrigin(t *testing.T) {
	if !deliveryPilotFixtureOrigin("http://127.0.0.1:18080") {
		t.Fatal("literal local fixture origin was rejected")
	}
	for _, origin := range []string{
		"https://127.0.0.1:18080", "http://localhost:18080", "http://[::1]:18080",
		"http://192.0.2.1:18080", "http://127.0.0.1:80", "http://127.0.0.1:018080",
		"http://127.0.0.1:18080/", "http://127.0.0.1:18080?", "http://127.0.0.1:18080#",
		"http://127.0.0.1:18080@192.0.2.1:18080",
	} {
		if deliveryPilotFixtureOrigin(origin) {
			t.Errorf("accepted invalid fixture origin %q", origin)
		}
	}
}
