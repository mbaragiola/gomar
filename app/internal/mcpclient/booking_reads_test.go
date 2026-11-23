package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReservationSetupMenuSavedPreservesUnknownAndFalse(t *testing.T) {
	const base = `"venue_enabled":false,"public_feature_enabled":false,"timezone":"America/Argentina/Buenos_Aires"`
	for _, tc := range []struct {
		name, fields, expectedJSON string
	}{
		{"older server", `"venue_configured":true`, ""},
		{"no saved menu", `"venue_configured":true,"menu_saved":false`, "false"},
		{"saved at disabled venue", `"venue_configured":true,"menu_saved":true,"private_name":"hidden.pdf"`, "true"},
		{"inconsistent venue", `"venue_configured":false,"menu_saved":true`, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/mcp/entities/1/outlets/2/reservation-setup" || r.URL.RawQuery != "" {
					t.Error("unexpected reservation setup request")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{" + base + "," + tc.fields + "}"))
			}))
			defer server.Close()
			client, err := New(server.URL, fakeToken)
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.ReservationSetup(context.Background(), 1, 2)
			if tc.expectedJSON == "invalid" {
				if err != invalidResponse {
					t.Fatalf("expected invalid response, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(got)
			var fields map[string]json.RawMessage
			if err != nil || json.Unmarshal(wire, &fields) != nil ||
				string(fields["menu_saved"]) != tc.expectedJSON || fields["private_name"] != nil {
				t.Fatalf("unexpected reservation setup output: %s", wire)
			}
		})
	}
}
