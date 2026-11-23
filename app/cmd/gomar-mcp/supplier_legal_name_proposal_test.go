package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSupplierLegalNameStdioWire(t *testing.T) {
	const readToken = "RAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const proposalToken = "PAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const toolName = "gomar_propose_supplier_legal_name_change"
	const reqID = "e234c97f-6704-4b6e-a568-8a357d6fb572"
	var count atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != "POST" || r.URL.EscapedPath() != "/api/v1/mcp/entities/4/outlets/3/supplier-legal-name-proposals" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+proposalToken || r.Header.Get("Cookie") != "" {
			t.Error("unexpected scoped request")
			w.WriteHeader(403)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"request_id":"`+reqID+`","supplier":{"supplier_id":2,"expected_revision":1,"name":"Café de Prueba SA"}}` {
			t.Error("unexpected proposal body")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if count.Load() == 2 {
			w.WriteHeader(409)
			io.WriteString(w, "private-provider-value")
			return
		}
		w.WriteHeader(201)
		io.WriteString(w, `{"id":7,"status":"pending","expires_at":"2026-11-01T03:15:00Z","name":"private-provider-value"}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	write := func(name, key string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	args := []string{"--origin", server.URL, "--token-file", write("read", readToken), "--proposal-session-action=supplier_legal_name_change"}
	if err := runLive(t.Context(), args, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("accepted missing dedicated token file")
	}
	args = append(args, "--proposal-session-token-file", write("proposal", proposalToken))
	ur, uw := io.Pipe()
	dr, dw := io.Pipe()
	defer ur.Close()
	defer uw.Close()
	defer dr.Close()
	defer dw.Close()
	done := make(chan error, 1)
	go func() { defer dw.Close(); done <- runLive(t.Context(), args, ur, dw) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "supplier-qa", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: dr, Writer: uw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 42 {
		t.Fatal(listed, err)
	}
	var hasSupplierProposal, hasReconciliationSetup bool
	for _, tool := range listed.Tools {
		if tool.Name == toolName {
			hasSupplierProposal = true
			if tool.Annotations == nil || !tool.Annotations.IdempotentHint || tool.Annotations.ReadOnlyHint || !strings.Contains(tool.Description, "toda la empresa") {
				t.Fatal(tool)
			}
		} else if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("unexpected proposal tool", tool.Name)
		}
		if tool.Name == "gomar_read_reconciliation_setup" {
			hasReconciliationSetup = true
		}
	}
	if !hasSupplierProposal || !hasReconciliationSetup {
		t.Fatal("missing the named supplier proposal or reconciliation setup read")
	}
	call := func(supplier map[string]any, want string, wantError bool) {
		t.Helper()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: toolName, Arguments: map[string]any{"entity_id": 4, "outlet_id": 3, "request_id": reqID, "supplier": supplier}})
		if err != nil || result.IsError != wantError {
			t.Fatal(result, err)
		}
		wire, _ := json.Marshal(result)
		if strings.Contains(string(wire), "private-provider-value") || strings.Contains(string(wire), "Café de Prueba") || !strings.Contains(string(wire), want) {
			t.Fatal("unexpected response")
		}
	}
	call(map[string]any{"supplier_id": 2, "name": "Café de Prueba SA"}, "", true)
	call(map[string]any{"supplier_id": 2, "expected_revision": nil, "name": "Café de Prueba SA"}, "", true)
	call(map[string]any{"supplier_id": 2, "expected_revision": 0, "name": "Café de Prueba SA"}, "", true)
	call(map[string]any{"supplier_id": 2, "expected_revision": 1, "name": "Café\u0000 SA"}, "", true)
	call(map[string]any{"supplier_id": 2, "expected_revision": 1, "name": "Café de Prueba SA", "cuit": "extra"}, "", true)
	input := map[string]any{"supplier_id": 2, "expected_revision": 1, "name": "  Café de Prueba SA "}
	call(input, `"status":"pending"`, false)
	call(input, "volvé a consultar el proveedor", true)
	if count.Load() != 2 {
		t.Fatal("unexpected upstream request count", count.Load())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
