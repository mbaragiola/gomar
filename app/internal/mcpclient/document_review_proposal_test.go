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

func TestDocumentReviewProposalSendsOnlyRevisionAndReason(t *testing.T) {
	const requestID = "cddd822c-c9bf-4187-a98d-d37b9e6d95f7"
	const fixtureKey = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var got struct {
			RequestID string         `json:"request_id"`
			Review    DocumentReview `json:"review"`
		}
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/document-review-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&got) != nil || got.RequestID != requestID ||
			got.Review.DocumentID != 15 || got.Review.ExpectedRevision == nil || *got.Review.ExpectedRevision != 0 || got.Review.Reason != "Control de prueba" {
			t.Error("unexpected review request")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","document_id":15,"total_minor":500}`)
	}))
	defer web.Close()
	client, err := NewProposalClient(web.URL, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	legacyRevision := int64(0)
	input := DocumentReview{DocumentID: 15, ExpectedRevision: &legacyRevision, Reason: " Control de prueba "}
	receipt, err := client.ProposeDocumentReview(t.Context(), 4, 3, requestID, input)
	if err != nil || receipt != (ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal("expected only the typed proposal receipt", err)
	}
	for _, reason := range []string{"", "  ", strings.Repeat("á", 513)} {
		input.Reason = reason
		if _, err := client.ProposeDocumentReview(t.Context(), 4, 3, requestID, input); err == nil {
			t.Fatal("invalid review reason sent")
		}
	}
	input.Reason, input.ExpectedRevision = "Control de prueba", nil
	if _, err := client.ProposeDocumentReview(t.Context(), 4, 3, requestID, input); err == nil {
		t.Fatal("absent revision accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("an invalid request reached Gomar")
	}
}
