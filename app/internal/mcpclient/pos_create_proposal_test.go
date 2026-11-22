package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type posCreateRoundTrip func(*http.Request) (*http.Response, error)

func (f posCreateRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPOSCreateProposalWire(t *testing.T) {
	const fixtureKey = "LAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const requestID = "e234c97f-6704-4b6e-a568-8a357d6fb572"
	no := false
	client, err := NewProposalClient("https://example.com", fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client.transport.http.Transport = posCreateRoundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		var body struct {
			RequestID string           `json:"request_id"`
			Product   POSProductCreate `json:"product"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/pos-create-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&body) != nil || body.RequestID != requestID || body.Product.Name != "Pan" || body.Product.PriceMinor != 199 || body.Product.Active == nil || *body.Product.Active {
			t.Fatal("unexpected POS-create wire")
		}
		return &http.Response{StatusCode: 201, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","product_id":99}`))}, nil
	})
	receipt, err := client.ProposePOSProductCreate(t.Context(), 4, 3, requestID, POSProductCreate{" Pan ", 199, &no})
	if err != nil || receipt != (ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal(receipt, err)
	}
	for _, in := range []POSProductCreate{{"Pan", 199, nil}, {"Pan", 0, &no}, {"Pan", 100000001, &no}, {" ", 1, &no}, {"x\x00", 1, &no}, {strings.Repeat("ñ", 121), 1, &no}, {string([]byte{0xff}), 1, &no}} {
		if _, err := client.ProposePOSProductCreate(t.Context(), 4, 3, requestID, in); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := client.ProposePOSProductCreate(t.Context(), 4, 3, "invalid", POSProductCreate{"Pan", 199, &no}); err == nil {
		t.Fatal("invalid request id")
	}
	if requests != 1 {
		t.Fatal(requests)
	}
}
