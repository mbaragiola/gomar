package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestStockReceiptProposalOnlyTransmitsPositiveAddition(t *testing.T) {
	const requestID = "37f7bed9-7be3-4acb-8ea3-2b484a87dce7"
	const fixtureKey = "EAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var got struct {
			RequestID string       `json:"request_id"`
			Receipt   StockReceipt `json:"receipt"`
		}
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/stock-receipt-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&got) != nil || got.RequestID != requestID ||
			got.Receipt != (StockReceipt{ProductID: 7, Quantity: 9}) {
			t.Error("unexpected stock receipt request")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","movement_id":99,"digest":"private"}`)
	}))
	defer server.Close()
	client, err := NewProposalClient(server.URL, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.ProposeStockReceipt(t.Context(), 4, 3, requestID, StockReceipt{ProductID: 7, Quantity: 9})
	if err != nil || receipt != (ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal("expected only the typed pending proposal receipt", err)
	}
	for _, qty := range []int64{0, -1, 1000001} {
		if _, err := client.ProposeStockReceipt(t.Context(), 4, 3, requestID, StockReceipt{ProductID: 7, Quantity: qty}); err == nil {
			t.Fatal("out-of-range addition accepted")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid addition sent")
	}
}
