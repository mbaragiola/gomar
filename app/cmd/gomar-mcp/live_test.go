package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLiveStdioScopedTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const secret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var requests atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Method != "GET" || r.Header.Get("Cookie") != "" {
			t.Error("invalid HTTP request")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/mcp/me":
			_, _ = io.WriteString(w, `{"id":7,"username":"prueba","unexpected":"`+secret+`"}`)
		case "/api/v1/mcp/entities":
			_, _ = io.WriteString(w, `{"entities":[{"id":20,"name":"Ficción"}]}`)
		case "/api/v1/mcp/entities/20/outlets":
			_, _ = io.WriteString(w, `{"outlets":[{"id":10,"name":"Centro"}]}`)
		case "/api/v1/mcp/entities/20/sales-summary":
			if r.URL.RawQuery != "outlet_id=10&period=this_month" {
				t.Errorf("incorrect sales query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"period":"this_month","timezone":"America/Argentina/Buenos_Aires","from":"2026-11-01T00:00:00-03:00","to":"2026-12-01T00:00:00-03:00","currency":"ARS","sale_count":1,"total_minor":1050,"outlets":[{"id":10,"name":"Centro","sale_count":1,"total_minor":1050}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, secret)
		}
	}))
	defer web.Close()
	path := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(path, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	uploadReader, uploadWriter := io.Pipe()
	downloadReader, downloadWriter := io.Pipe()
	defer uploadReader.Close()
	defer uploadWriter.Close()
	defer downloadReader.Close()
	defer downloadWriter.Close()
	finished := make(chan error, 1)
	go func() {
		defer downloadWriter.Close()
		finished <- runLive(ctx, []string{"--origin", web.URL, "--token-file", path}, uploadReader, downloadWriter)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "qa", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: downloadReader, Writer: uploadWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	metadata, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil || len(metadata.Tools) != 4 {
		t.Fatal(metadata, err)
	}
	for _, tool := range metadata.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %s isn't strictly read-only", tool.Name)
		}
	}
	for _, test := range []struct {
		name    string
		args    map[string]any
		want    string
		isError bool
	}{
		{"gomar_current_user", nil, `"username":"prueba"`, false},
		{"gomar_list_entities", nil, `"id":20`, false},
		{"gomar_list_outlets", map[string]any{"entity_id": 20}, `"name":"Centro"`, false},
		{"gomar_sales_summary", map[string]any{"entity_id": 20, "outlet_id": 10, "period": "this_month"}, `"total_minor":1050`, false},
		{"gomar_sales_summary", map[string]any{"entity_id": 21, "period": "this_month"}, "permisos", true},
		{"gomar_sales_summary", map[string]any{"entity_id": 20, "period": "this_month", "path": "http://forged.invalid"}, "", true},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: test.name, Arguments: test.args})
		if err != nil {
			t.Fatal(test.name, err)
		}
		if len(result.Content) != 1 {
			t.Fatal(test.name, result)
		}
		block, ok := result.Content[0].(*mcp.TextContent)
		wire, _ := json.Marshal(result)
		if !ok || result.IsError != test.isError || !strings.Contains(block.Text, test.want) || strings.Contains(string(wire), secret) ||
			(!test.isError && result.StructuredContent == nil) {
			t.Fatalf("unexpected result for %s: %s", test.name, wire)
		}
	}
	if requests.Load() != 5 {
		t.Fatalf("wrong number of requests: %d", requests.Load())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
