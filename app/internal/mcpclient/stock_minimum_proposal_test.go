package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type stockMinRoundTrip func(*http.Request) (*http.Response, error)

func (f stockMinRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStockMinimumProposalWire(t *testing.T) {
	const fixtureKey = "LAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const requestID = "e234c97f-6704-4b6e-a568-8a357d6fb572"
	zero, negative, excessive := int64(0), int64(-1), int64(1000001)
	client, err := NewProposalClient("https://example.com", fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client.transport.http.Transport = stockMinRoundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		var body struct {
			RequestID string             `json:"request_id"`
			Change    StockMinimumChange `json:"minimum_change"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/stock-minimum-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&body) != nil || body.RequestID != requestID || body.Change.ProductID != 7 || body.Change.ExpectedRevision != 2 || body.Change.Minimum == nil || *body.Change.Minimum != 0 {
			t.Fatal("unexpected stock minimum proposal wire")
		}
		return &http.Response{StatusCode: 201, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","product_id":99}`))}, nil
	})
	receipt, err := client.ProposeStockMinimumChange(t.Context(), 4, 3, requestID, StockMinimumChange{7, 2, &zero})
	if err != nil || receipt != (ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal(receipt, err)
	}
	for _, in := range []StockMinimumChange{{7, 2, nil}, {0, 2, &zero}, {7, 0, &zero}, {7, 1<<63 - 1, &zero}, {7, 2, &negative}, {7, 2, &excessive}} {
		if _, err := client.ProposeStockMinimumChange(t.Context(), 4, 3, requestID, in); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := client.ProposeStockMinimumChange(t.Context(), 4, 3, "invalid", StockMinimumChange{7, 2, &zero}); err == nil {
		t.Fatal("invalid request id")
	}
	if requests != 1 {
		t.Fatal(requests)
	}
}
