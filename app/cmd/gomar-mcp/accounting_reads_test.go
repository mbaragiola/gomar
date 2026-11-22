package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAccountingReadToolsUseScopedGETAndTypedOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const prefix = "/api/v1/mcp/entities/14/outlets/16"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+token || !strings.HasPrefix(r.URL.Path, prefix+"/") {
			t.Error("not a scoped GET")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var response string
		switch strings.TrimPrefix(r.URL.Path, prefix) {
		case "/documents":
			if r.URL.RawQuery != "after_id=3" {
				t.Error("document cursor was not passed correctly")
			}
			response = `{"documents":[{"id":4,"type":"credit_note","status":"reviewed","supplier_id":1,"issue_date":"2026-11-01","currency":"ARS","total_minor":25,"attachment":"private sentinel"}]}`
		case "/suppliers":
			response = `{"suppliers":[{"id":1,"name":"Synthetic supplier","cuit":"private sentinel"}]}`
		case "/payments-summary":
			response = `{"draft":1,"posted":3,"reversed":2,"reversals":2,"receipt":"private sentinel"}`
		case "/reconciliation-summary":
			response = `{"draft":2,"review":1,"closed":1,"account":"private sentinel"}`
		default:
			t.Error("unexpected path")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	httpClient, err := mcpclient.New(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	handler := mcp.NewServer(&mcp.Implementation{Name: "test-gomar", Version: "1"}, nil)
	addAccountingReadTools(handler, httpClient)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv, err := handler.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	meta, err := session.ListTools(ctx, nil)
	if err != nil || len(meta.Tools) != 4 {
		t.Fatal(meta, err)
	}
	for _, tool := range meta.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("%s was not declared a read-only local-data tool", tool.Name)
		}
	}
	for name, field := range map[string]string{
		"gomar_list_documents": `"type":"credit_note"`, "gomar_list_suppliers": `"name":"Synthetic supplier"`,
		"gomar_payment_status_summary": `"posted":3`, "gomar_reconciliation_status_summary": `"review":1`,
	} {
		args := map[string]any{"entity_id": 14, "outlet_id": 16}
		if name == "gomar_list_documents" {
			args["after_id"] = 3
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatal(name, result, err)
		}
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil || !strings.Contains(string(raw), field) || strings.Contains(string(raw), "private sentinel") {
			t.Fatalf("%s: unexpected typed result: %s", name, raw)
		}
	}
}
