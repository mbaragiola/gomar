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
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWikiProposalToolNeedsSeparateOptInAndOnlyQueues(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const readKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const proposalKey = "BAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const requestID = "7a0a2537-9c03-452d-8e28-93bb289c812a"
	const seal = "private-synthetic-field"
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/20/outlets/10/wiki-create-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+proposalKey || r.Header.Get("Cookie") != "" {
			t.Error("unexpected request")
			w.WriteHeader(400)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 3 || body["request_id"] != requestID ||
			body["title"] != "Turno" || body["body"] != "Notas de prueba" {
			t.Error("unexpected proposal fields")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":7,"status":"pending","expires_at":"2026-11-01T03:15:00Z","digest":"`+seal+`"}`)
	}))
	defer web.Close()
	dir := t.TempDir()
	readFile := filepath.Join(dir, "read")
	proposalFile := filepath.Join(dir, "proposal")
	for path, key := range map[string]string{readFile: readKey, proposalFile: proposalKey} {
		if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	start := []string{"--origin", web.URL, "--token-file", readFile}
	if err := runLive(ctx, append(append([]string(nil), start...), "--wiki-proposal-token-file", readFile), strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("same credential accepted for reads and proposals")
	}
	for _, optIn := range []bool{false, true} {
		args := append([]string(nil), start...)
		if optIn {
			args = append(args, "--wiki-proposal-token-file", proposalFile)
		}
		upr, upw := io.Pipe()
		dnr, dnw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			defer dnw.Close()
			done <- runLive(ctx, args, upr, dnw)
		}()
		client := mcp.NewClient(&mcp.Implementation{Name: "qa", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.IOTransport{Reader: dnr, Writer: upw}, nil)
		if err != nil {
			t.Fatal(err)
		}
		list, err := session.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			t.Fatal(err)
		}
		var visible bool
		for _, tool := range list.Tools {
			if tool.Name == "gomar_propose_wiki_page" {
				visible = true
				if tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
					t.Fatal("proposal is not labelled as an additive queue operation")
				}
			}
		}
		if visible != optIn {
			t.Fatal("proposal visibility did not follow explicit configuration")
		}
		if optIn {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gomar_propose_wiki_page", Arguments: map[string]any{
				"entity_id": 20, "outlet_id": 10, "request_id": requestID, "title": " Turno ", "body": "Notas de prueba",
			}})
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(result)
			if result.IsError || result.StructuredContent == nil || !strings.Contains(string(wire), "\"status\":\"pending\"") ||
				strings.Contains(string(wire), seal) || strings.Contains(string(wire), proposalKey) {
				t.Fatal("proposal did not return an isolated typed pending receipt")
			}
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		_ = upr.Close()
		_ = upw.Close()
		_ = dnr.Close()
		_ = dnw.Close()
	}
}
