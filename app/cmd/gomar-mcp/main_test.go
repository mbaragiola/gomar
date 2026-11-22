package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestStdioScopeAndLifecycle(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":"probe","method":"server/discover"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"setup","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_payables_summary","arguments":{"alcance":"norte"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_payables_summary","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"ping"}`,
		"",
	}, "\n")
	var out bytes.Buffer
	if err := run([]string{"--synthetic-scope=centro"}, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	wantIDs := []string{`"probe"`, "1", `"setup"`, "2", "3", "4", "5", "6"}
	if len(lines) != len(wantIDs) {
		t.Fatalf("expected %d protocol frames; got %d", len(wantIDs), len(lines))
	}
	for i, line := range lines {
		var got struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
		}
		if json.Unmarshal([]byte(line), &got) != nil || got.JSONRPC != "2.0" || string(got.ID) != wantIDs[i] {
			t.Fatalf("frame %d invalid: %q", i, line)
		}
	}
	for position, fragment := range map[int]string{
		0: `"code":-32601`, 1: `"code":-32002`, 2: `"protocolVersion":"2025-06-18"`, 3: `"code":-32002`,
		4: `"name":"get_payables_summary"`, 5: `"isError":true`, 7: `"result":{}`,
	} {
		if !strings.Contains(lines[position], fragment) {
			t.Fatalf("frame %d missing %s: %s", position, fragment, lines[position])
		}
	}
	var call struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(lines[6]), &call) != nil || call.Result.IsError || len(call.Result.Content) != 1 {
		t.Fatalf("bad tool result: %s", lines[6])
	}
	if !strings.Contains(call.Result.Content[0].Text, `"alcance":"centro"`) ||
		!strings.Contains(call.Result.Content[0].Text, `"saldo_centavos":16000`) {
		t.Fatalf("summary scope or totals changed: %s", call.Result.Content[0].Text)
	}
}

func TestStdioRefusesBadScopeAndOversizeFrame(t *testing.T) {
	for _, args := range [][]string{nil, {"--synthetic-scope=unknown"}, {"--synthetic-scope=norte", "--extra"}} {
		var out bytes.Buffer
		if err := run(args, strings.NewReader(""), &out); err == nil || out.Len() != 0 {
			t.Fatalf("invalid config accepted: %v %q", err, out.String())
		}
	}
	var out bytes.Buffer
	if err := run([]string{"--synthetic-scope=norte"}, strings.NewReader(strings.Repeat("x", 16*1024+1)+"\n"), &out); err == nil || out.Len() != 0 {
		t.Fatalf("oversize frame accepted: %v %q", err, out.String())
	}
}
