package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWikiEditProposalUsesExactVersionAndReturnsOnlyReceipt(t *testing.T) {
	const requestID = "f5023b2a-f5d7-4c10-9b32-3e4f86a889aa"
	const fixtureKey = "DAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const text = "  exacto\nturno  "
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var got struct {
			RequestID string   `json:"request_id"`
			Edit      WikiEdit `json:"edit"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/6/outlets/2/wiki-edit-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			decoder.Decode(&got) != nil || got.RequestID != requestID ||
			got.Edit != (WikiEdit{ArticleID: 9, ExpectedVersion: 2, Title: "Manual", Body: text}) {
			t.Error("wrong wiki edit request")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if n == 2 {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"detail":"private error detail"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":3,"status":"pending","expires_at":"2026-11-01T03:15:00Z","article_version":99,"digest":"private","before":"private"}`)
	}))
	defer web.Close()
	client, err := NewProposalClient(web.URL, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	edit := WikiEdit{ArticleID: 9, ExpectedVersion: 2, Title: " Manual ", Body: text}
	receipt, err := client.ProposeWikiEdit(t.Context(), 6, 2, requestID, edit)
	if err != nil || receipt != (ProposalReceipt{ID: 3, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal("expected only the typed proposal receipt", err)
	}
	if _, err := client.ProposeWikiEdit(t.Context(), 6, 2, requestID, edit); err == nil ||
		!strings.Contains(err.Error(), "volvé a leer la página") || strings.Contains(err.Error(), "private") {
		t.Fatal("expected a safe instruction for a stale wiki edit", err)
	}
	edit.ExpectedVersion = 0
	if _, err := client.ProposeWikiEdit(t.Context(), 6, 2, requestID, edit); err == nil || calls.Load() != 2 {
		t.Fatal("invalid version sent")
	}
}
