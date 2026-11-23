package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPOSTableCheckRequiresExactSameOriginHandoff(t *testing.T) {
	const native = "/pos#entity=1&outlet=2&table=7"
	const api = "/api/v1/mcp/entities/1/outlets/2/pos-table-checks/7"
	for _, tt := range []struct {
		name, link, state string
		wantOK            bool
	}{
		{"exact", native, "untendered", true},
		{"manual records", native, "manual_records", true},
		{"different outlet", "/pos#entity=1&outlet=3&table=7", "untendered", false},
		{"different table", "/pos#entity=1&outlet=2&table=8", "untendered", false},
		{"absolute address", "https://example.com" + native, "untendered", false},
		{"unexpected status", native, "recorded", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != api || r.URL.RawQuery != "" {
					t.Error("unexpected request")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"table_number": 7, "total_minor": 1234, "handoff_url": tt.link, "state": tt.state,
					"items": []string{"must-not-escape"},
				})
			}))
			defer server.Close()
			client, err := New(server.URL, fakeToken)
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.ReadPOSTableCheck(context.Background(), 1, 2, 7)
			if !tt.wantOK {
				if err != invalidResponse || got != (POSTableCheck{}) {
					t.Fatalf("expected no result for a wrong envelope: %+v %v", got, err)
				}
				return
			}
			if err != nil || got != (POSTableCheck{7, 1234, "untendered", native}) {
				t.Fatalf("wrong result: %+v %v", got, err)
			}
		})
	}
}
