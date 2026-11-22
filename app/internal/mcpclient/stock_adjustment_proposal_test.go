package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestStockAdjustmentProposalsPreserveExplicitCountZero(t *testing.T) {
	const requestID = "aa76a6ec-113e-4637-bc9b-2a1e54822595"
	const fixtureKey = "JAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var entry struct {
			RequestID  string          `json:"request_id"`
			Adjustment StockAdjustment `json:"adjustment"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		n := calls.Add(1)
		path, quantity := "stock-physical-count-proposals", int64(0)
		if n == 2 {
			path, quantity = "stock-waste-proposals", 3
		}
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/"+path ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&entry) != nil || entry.RequestID != requestID || entry.Adjustment.ProductID != 15 ||
			entry.Adjustment.Quantity == nil || *entry.Adjustment.Quantity != quantity {
			t.Error("unexpected stock adjustment request")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","movement_id":99}`)
	}))
	defer web.Close()
	client, err := NewProposalClient(web.URL, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	want := ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}
	zero, three, negative := int64(0), int64(3), int64(-1)
	if out, err := client.ProposeStockPhysicalCount(t.Context(), 4, 3, requestID, StockAdjustment{ProductID: 15, Quantity: &zero}); err != nil || out != want {
		t.Fatal("expected only pending receipt for explicit zero count", err)
	}
	if out, err := client.ProposeStockWaste(t.Context(), 4, 3, requestID, StockAdjustment{ProductID: 15, Quantity: &three}); err != nil || out != want {
		t.Fatal("expected only pending receipt for waste", err)
	}
	if _, err := client.ProposeStockWaste(t.Context(), 4, 3, requestID, StockAdjustment{ProductID: 15, Quantity: &zero}); err == nil {
		t.Fatal("zero waste accepted")
	}
	for _, value := range []*int64{nil, &negative} {
		if _, err := client.ProposeStockPhysicalCount(t.Context(), 4, 3, requestID, StockAdjustment{ProductID: 15, Quantity: value}); err == nil {
			t.Fatal("invalid count accepted")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("invalid request sent")
	}
}
