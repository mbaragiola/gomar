package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type discardOutput struct{ io.Writer }

func (discardOutput) Close() error { return nil }

func TestDemoFlagMustBeExplicitAndAlone(t *testing.T) {
	for _, args := range [][]string{nil, {"--synthetic-demo", "/example"}, {"--synthetic-demo=true"}, {"--synthetic-demo", "--synthetic-demo"}, {"--synthetic-claude", "--synthetic-demo"}, {"--synthetic-whatsapp", "--synthetic-claude"}, {"--synthetic-claude=true"}, {"--synthetic-claude", "--listen=0.0.0.0:80"}} {
		err := run(t.Context(), args, io.NopCloser(strings.NewReader("")), discardOutput{io.Discard})
		if err == nil || err.Error() != usage {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestPreviewSubprocess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestPreviewProcessHelper$")
	cmd.Env = []string{"GOMAR_PREVIEW_TEST_PROCESS=1"}
	client := mcp.NewClient(&mcp.Implementation{Name: "process-check", Version: "1"}, nil)
	conn, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	res, err := conn.CallTool(ctx, &mcp.CallToolParams{Name: "get_payables_summary"})
	if err != nil || res.IsError || len(res.Content) != 1 {
		t.Fatalf("subprocess tool response: %#v, %v", res, err)
	}
	content, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(content.Text, `"datos_sinteticos":true`) || !strings.Contains(content.Text, `"saldo_centavos":20950`) {
		t.Fatalf("unexpected subprocess totals: %#v", res.Content)
	}
}

func TestPreviewProcessHelper(t *testing.T) {
	if os.Getenv("GOMAR_PREVIEW_TEST_PROCESS") != "1" {
		return
	}
	if err := run(t.Context(), []string{"--synthetic-demo"}, os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
