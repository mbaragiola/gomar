package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestReconciliationProposalRequiresExplicitReferenceOption(t *testing.T) {
	const requestID = "2f9fde8f-792d-4257-ba36-71bb0d165532"
	const fixtureKey = "HAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var entry struct {
			RequestID string              `json:"request_id"`
			Draft     ReconciliationDraft `json:"draft"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/reconciliation-draft-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&entry) != nil || entry.RequestID != requestID ||
			entry.Draft.MatchReference == nil || *entry.Draft.MatchReference {
			t.Error("unexpected reconciliation draft request")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","run_id":99,"digest":"private"}`)
	}))
	defer web.Close()
	client, err := NewProposalClient(web.URL, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	no := false
	receipt, err := client.ProposeReconciliationDraft(t.Context(), 4, 3, requestID, ReconciliationDraft{MatchReference: &no})
	if err != nil || receipt != (ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal("expected only the typed proposal receipt", err)
	}
	if _, err := client.ProposeReconciliationDraft(t.Context(), 4, 3, requestID, ReconciliationDraft{}); err == nil || calls.Load() != 1 {
		t.Fatal("absent reference option sent")
	}
}
