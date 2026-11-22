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

func TestDocumentProposalToolOnlySendsTypedDraftWithItsOwnKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	const readKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const draftKey = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const requestID = "b172daf1-1ae6-4e60-89b8-74bb165af189"
	const forbidden = "fixture-private"
	var sent atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/document-draft-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+draftKey || r.Header.Get("Cookie") != "" {
			t.Error("unexpected document proposal route or credential")
			w.WriteHeader(422)
			return
		}
		var entry struct {
			RequestID string         `json:"request_id"`
			Draft     map[string]any `json:"draft"`
		}
		d := json.NewDecoder(r.Body)
		d.UseNumber()
		d.DisallowUnknownFields()
		if err := d.Decode(&entry); err != nil || entry.RequestID != requestID || len(entry.Draft) != 9 ||
			entry.Draft["total_minor"] != json.Number("12112") ||
			entry.Draft["supplier_id"] != json.Number("2") || entry.Draft["letter"] != "A" {
			t.Error("unexpected document proposal payload")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","document_id":99,"digest":"`+forbidden+`"}`)
	}))
	defer web.Close()
	dir := t.TempDir()
	readFile, draftFile := filepath.Join(dir, "read"), filepath.Join(dir, "draft")
	for path, key := range map[string]string{readFile: readKey, draftFile: draftKey} {
		if err := os.WriteFile(path, []byte(key), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--origin", web.URL, "--token-file", readFile, "--document-proposal-token-file", draftFile}
	duplicate := append(append([]string(nil), args...), "--wiki-proposal-token-file", draftFile)
	if err := runLive(ctx, duplicate, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("one credential accepted for both proposal purposes")
	}
	upr, upw := io.Pipe()
	dnr, dnw := io.Pipe()
	defer upr.Close()
	defer upw.Close()
	defer dnr.Close()
	defer dnw.Close()
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
	for _, tool := range list.Tools {
		if tool.Name == "gomar_propose_wiki_page" {
			t.Fatal("document configuration advertised wiki proposal")
		}
	}
	draft := map[string]any{"supplier_id": 2, "type": "invoice", "letter": "a", "point_of_sale": 3, "number": 40,
		"issue_date": "2026-10-05", "currency": "ARS", "net_minor": 10010, "total_minor": 12112}
	params := &mcp.CallToolParams{Name: "gomar_propose_document_draft",
		Arguments: map[string]any{"entity_id": 4, "outlet_id": 3, "request_id": requestID, "draft": draft}}
	result, err := session.CallTool(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(result)
	if result.IsError || result.StructuredContent == nil || !strings.Contains(string(wire), "\"status\":\"pending\"") ||
		strings.Contains(string(wire), forbidden) || strings.Contains(string(wire), "document_id") ||
		strings.Contains(string(wire), draftKey) {
		t.Fatal("the result did not contain just the pending receipt")
	}
	for name, value := range map[string]any{"cuit": forbidden, "total_minor": -50} {
		original, existed := draft[name]
		draft[name] = value
		if bad, err := session.CallTool(ctx, params); err != nil || !bad.IsError {
			t.Fatal("invalid document input was accepted")
		}
		if existed {
			draft[name] = original
		} else {
			delete(draft, name)
		}
	}
	if sent.Load() != 1 {
		t.Fatal("invalid proposal was sent")
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
}
