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

func TestPOSRenameProposalOnlySendsNameAndExactRevision(t *testing.T) {
	const requestID = "123e4567-e89b-42d3-a456-426614174000"
	const key = "FAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			RequestID string    `json:"request_id"`
			Rename    POSRename `json:"rename"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		n := calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/pos-rename-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("Cookie") != "" ||
			d.Decode(&got) != nil || got.RequestID != requestID ||
			got.Rename != (POSRename{ProductID: 15, ExpectedRevision: 2, Name: "Pan de campo"}) {
			t.Error("unexpected product proposal request")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if n == 2 {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"detail":"private"}`)
			return
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","product_id":15,"unchanged_price_minor":500}`)
	}))
	defer web.Close()
	client, err := NewProposalClient(web.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	data := POSRename{ProductID: 15, ExpectedRevision: 2, Name: " Pan de campo "}
	receipt, err := client.ProposePOSRename(t.Context(), 4, 3, requestID, data)
	if err != nil || receipt != (ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal("expected only the typed proposal receipt", err)
	}
	if _, err := client.ProposePOSRename(t.Context(), 4, 3, requestID, data); err == nil ||
		!strings.Contains(err.Error(), "revisión") || strings.Contains(err.Error(), "private") {
		t.Fatal("expected safe stale guidance", err)
	}
	data.ExpectedRevision = 0
	if _, err := client.ProposePOSRename(t.Context(), 4, 3, requestID, data); err == nil || calls.Load() != 2 {
		t.Fatal("invalid revision sent")
	}
}
