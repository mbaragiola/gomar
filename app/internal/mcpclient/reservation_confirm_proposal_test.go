package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestReservationConfirmationOnlyUsesExactStaffReference(t *testing.T) {
	const requestID = "e234c97f-6704-4b6e-a568-8a357d6fb572"
	const fixtureKey = "LAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const reference = "4217ea788f2d492c8a3767b631c93331"
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var entry struct {
			RequestID    string                  `json:"request_id"`
			Confirmation ReservationConfirmation `json:"confirmation"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if r.Method != "POST" || r.URL.Path != "/api/v1/mcp/entities/4/outlets/3/reservation-confirm-proposals" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+fixtureKey || r.Header.Get("Cookie") != "" ||
			d.Decode(&entry) != nil || entry.RequestID != requestID || entry.Confirmation.Reference != reference {
			t.Error("unexpected typed request")
			w.WriteHeader(422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":12,"status":"pending","expires_at":"2026-11-01T03:15:00Z","reservation_reference":"`+reference+`"}`)
	}))
	defer web.Close()
	client, err := NewProposalClient(web.URL, fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.ProposeReservationConfirmation(t.Context(), 4, 3, requestID, ReservationConfirmation{reference})
	if err != nil || receipt != (ProposalReceipt{ID: 12, Status: "pending", ExpiresAt: "2026-11-01T03:15:00Z"}) {
		t.Fatal("expected only typed pending receipt", err)
	}
	if _, err := client.ProposeReservationConfirmation(t.Context(), 4, 3, requestID, ReservationConfirmation{"missing"}); err == nil || calls.Load() != 1 {
		t.Fatal("invalid staff reference sent")
	}
}
