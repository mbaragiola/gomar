package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDocumentEditProposalTransmitsOnlyDatesAmountsAndReason(t *testing.T) {
	const requestID = "e234c97f-6704-4b6e-a568-8a357d6fb572"
	const fixtureKey = "IAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var entry struct {
			RequestID string         `json:"request_id"`
			Edit      map[string]any `json:"edit"`
		}
		d := json.NewDecoder(r.Body)
		d.UseNumber()
		d.DisallowUnknownFields()
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/document-edit-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&entry) != nil || entry.RequestID != requestID ||
			len(entry.Edit) != 4 || entry.Edit["document_id"] != json.Number("15") ||
			entry.Edit["expected_revision"] != json.Number("0") || entry.Edit["reason"] != "Corregir fecha" {
			t.Error("unexpected document edit request")
			w.WriteHeader(422)
			return
		}
		after, ok := entry.Edit["after"].(map[string]any)
		if !ok || len(after) != 5 || after["total_minor"] != json.Number("777") ||
			after["issue_date"] != "2026-11-01" || after["due_date"] != nil || after["net_minor"] != nil || after["tax_minor"] != nil {
			t.Error("expected explicit nulls and exact amount")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","document_id":15}`)
	}))
	defer web.Close()
	client, err := NewProposalClient(web.URL, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	zero, total := int64(0), int64(777)
	edit := DocumentEdit{DocumentID: 15, ExpectedRevision: &zero, Reason: " Corregir fecha ",
		After: DocumentEditValues{IssueDate: "2026-11-01", Total: &total}}
	receipt, err := client.ProposeDocumentEdit(t.Context(), 4, 3, requestID, edit)
	if err != nil || receipt != (ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal("expected only typed pending receipt", err)
	}
	dueEarly := "2026-10-31"
	net, tax := int64(555), int64(111)
	for _, values := range []DocumentEditValues{
		{IssueDate: "2026-11-01", Total: nil},
		{IssueDate: "2026-11-01", DueDate: &dueEarly, Total: &total},
		{IssueDate: "2026-11-01", Net: &net, Total: &total},
		{IssueDate: "2026-11-01", Net: &net, Tax: &tax, Total: &total},
	} {
		edit.After = values
		if _, err := client.ProposeDocumentEdit(t.Context(), 4, 3, requestID, edit); err == nil {
			t.Fatal("invalid edit sent")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid edit reached Gomar")
	}
}
