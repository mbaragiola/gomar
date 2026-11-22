package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPOSCatalogProposalsKeepPriceAndAvailabilitySeparate(t *testing.T) {
	const requestID = "37f7bed9-7be3-4acb-8ea3-2b484a87dce7"
	const fixtureKey = "KAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var got struct {
			RequestID string         `json:"request_id"`
			Change    map[string]any `json:"change"`
		}
		d := json.NewDecoder(r.Body)
		d.UseNumber()
		d.DisallowUnknownFields()
		path := "pos-price-proposals"
		if n == 2 {
			path = "pos-availability-proposals"
		}
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/"+path ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&got) != nil || got.RequestID != requestID || len(got.Change) != 3 ||
			got.Change["product_id"] != json.Number("15") || got.Change["expected_revision"] != json.Number("2") ||
			(n == 1 && got.Change["price_minor"] != json.Number("8750")) || (n == 2 && got.Change["active"] != false) {
			t.Error("wrong typed product change")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","product_revision":99}`)
	}))
	defer web.Close()
	client, err := NewProposalClient(web.URL, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	want := ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}
	no := false
	if out, err := client.ProposePOSPrice(t.Context(), 4, 3, requestID, POSPriceChange{15, 2, 8750}); err != nil || out != want {
		t.Fatal("expected only a typed pending price receipt", err)
	}
	if out, err := client.ProposePOSAvailability(t.Context(), 4, 3, requestID, POSAvailabilityChange{15, 2, &no}); err != nil || out != want {
		t.Fatal("expected only a typed pending availability receipt", err)
	}
	if _, err := client.ProposePOSPrice(t.Context(), 4, 3, requestID, POSPriceChange{15, 2, 0}); err == nil {
		t.Fatal("invalid price sent")
	}
	if _, err := client.ProposePOSAvailability(t.Context(), 4, 3, requestID, POSAvailabilityChange{15, 2, nil}); err == nil || calls.Load() != 2 {
		t.Fatal("missing availability sent")
	}
}
