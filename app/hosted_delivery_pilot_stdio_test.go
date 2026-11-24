package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The optional binary is built with go test -c ./cmd/gomar-mcp. Unlike the
// normal CLI, this test process cannot select a different catalog or origin.
func openDeliveryPilotStdio(t *testing.T, ctx context.Context, binary, origin, token string, client *mcp.Client) *mcp.ClientSession {
	t.Helper()
	if !filepath.IsAbs(binary) {
		t.Fatal("require the freshly built absolute delivery pilot test executable")
	}
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "fixture.key"), []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = scratch
	cmd.Env = []string{
		"GOMAR_QA_DELIVERY_STDIO=1", "GOMAR_QA_DELIVERY_ORIGIN=" + origin,
		"HOME=" + scratch, "LANG=C.UTF-8",
	}
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to delivery pilot test executable over stdio: %v", err)
	}
	t.Cleanup(func() {
		if err := cs.Close(); err != nil {
			t.Error("delivery pilot test executable did not close cleanly")
		}
	})
	t.Log("Connected to separately compiled delivery pilot over MCP stdio")
	return cs
}
