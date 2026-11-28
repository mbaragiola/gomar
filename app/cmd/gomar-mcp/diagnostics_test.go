package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolDiagnosticsRedactsParametersAndErrors(t *testing.T) {
	const fixture = "secret-fixture"
	var log bytes.Buffer
	logger := toolDiagnostics(&log)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		result *mcp.CallToolResult
		err    error
	}{
		{"gomar_current_user", &mcp.CallToolResult{}, nil},
		{"gomar_sales_summary", &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fixture + " [gomar:scope_unavailable]"}}}, nil},
		{fixture, nil, errors.New(fixture)},
	} {
		next := logger(func(context.Context, string, mcp.Request) (mcp.Result, error) {
			if tc.err != nil {
				return nil, tc.err
			}
			return tc.result, nil
		})
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: tc.name, Arguments: json.RawMessage(`{"name":"secret-fixture"}`)}}
		if _, err := next(ctx, "tools/call", req); err != tc.err {
			t.Fatal("diagnostics changed the response")
		}
	}
	body := strings.TrimSpace(log.String())
	lines := strings.Split(body, "\n")
	if len(lines) != 3 || strings.Contains(body, fixture) {
		t.Fatal("diagnostics did not preserve the field allowlist")
	}
	ids := map[string]bool{}
	for i, expected := range []string{"ok", "tool_error", "protocol_error"} {
		var event map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &event); err != nil {
			t.Fatal(err)
		}
		id, _ := event["call_id"].(string)
		if id == "" || ids[id] || event["outcome"] != expected || event["duration_ms"] == nil {
			t.Fatal("incomplete call diagnostics")
		}
		ids[id] = true
		if i == 1 && event["error_code"] != "scope_unavailable" {
			t.Fatal("diagnostics lost the safe failure category")
		}
		if i == 2 && event["tool"] != nil {
			t.Fatal("protocol failure recorded an untrusted name")
		}
	}
}
