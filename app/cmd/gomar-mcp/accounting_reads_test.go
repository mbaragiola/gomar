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
		case "/document-totals":
			if r.URL.RawQuery != "" {
				t.Error("totals passed a query")
			}
			response = `{"count":2,"totals":[{"currency":"ARS","count":1,"total_minor":"10000000000000000000","note":"private sentinel"},{"currency":"USD","count":1,"total_minor":"-125"}],"supplier":"private sentinel"}`
		case "/suppliers":
			response = `{"suppliers":[{"id":1,"name":"Synthetic supplier","revision":7,"cuit":"private sentinel"}]}`
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
	if err != nil || len(meta.Tools) != 5 {
		t.Fatal(meta, err)
	}
	for _, tool := range meta.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("%s was not declared a read-only local-data tool", tool.Name)
		}
	}
	for name, field := range map[string]string{
		"gomar_document_totals": `"total_minor":"10000000000000000000"`,
		"gomar_list_documents":  `"type":"credit_note"`, "gomar_list_suppliers": `"name":"Synthetic supplier","revision":7`,
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

func TestPrintedReferenceToolFailsClosedWhenFilterUnsupported(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const correct = `"printed_reference":{"document_type":"credit_note","letter":"","point_of_sale":12,"number":345}`
	const invalid = `"printed_reference":{"document_type":"credit_note","point_of_sale":12,"number":345}`
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/mcp/entities/14/outlets/16/documents" ||
			r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("not a scoped GET")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after_id") {
		case "9":
			if r.URL.RawQuery != "after_id=9&document_type=credit_note&letter=&number=345&point_of_sale=12" {
				t.Errorf("query = %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"documents":[{"id":10,"type":"credit_note","status":"reviewed","supplier_id":1,"issue_date":"2026-11-01","currency":"ARS","total_minor":25,"issuer_cuit":"sentinel"}],` + correct + `}`))
		case "10":
			_, _ = w.Write([]byte(`{"documents":[]}`))
		case "11":
			_, _ = w.Write([]byte(`{"documents":[],` + invalid + `}`))
		case "12":
			_, _ = w.Write([]byte(`{"documents":[],` + strings.Replace(correct, "345", "346", 1) + `}`))
		case "13":
			_, _ = w.Write([]byte(`{"documents":[],` + correct + `}`))
		default:
			t.Error("malformed tool input reached HTTP")
			w.WriteHeader(400)
		}
	}))
	defer httpServer.Close()
	client, err := mcpclient.New(httpServer.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test-gomar", Version: "1"}, nil)
	addAccountingReadTools(server, client)
	st, ct := mcp.NewInMemoryTransports()
	s, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, test := range []struct {
		after int
		ref   string
		ok    bool
	}{
		{9, correct, true}, {10, correct, false}, {11, correct, false}, {12, correct, false},
		{13, correct, true}, {14, invalid, false}, {14, `"printed_reference":null`, false},
		{14, strings.Replace(correct, "345", "0", 1), false},
		{14, strings.Replace(correct, `"letter":""`, `"letter":"","supplier":7`, 1), false},
	} {
		args := map[string]any{}
		if err := json.Unmarshal([]byte(`{"entity_id":14,"outlet_id":16,`+test.ref+`}`), &args); err != nil {
			t.Fatal(err)
		}
		args["after_id"] = test.after
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gomar_list_documents", Arguments: args})
		if test.ok {
			if err != nil || result.IsError {
				payload, _ := json.Marshal(result)
				t.Fatalf("expected supported filter after %d: %s %v", test.after, payload, err)
			}
			raw, err := json.Marshal(result.StructuredContent)
			if err != nil || !strings.Contains(string(raw), `"printed_reference"`) || strings.Contains(string(raw), "sentinel") {
				t.Fatalf("unexpected typed output: %s", raw)
			}
		} else if err == nil && !result.IsError {
			t.Fatalf("unsupported or invalid filter accepted after %d: %v", test.after, result)
		}
	}
}
