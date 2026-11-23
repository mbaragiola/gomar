package mcpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPayablePreflightSanitizesFixedResponse(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{`{"eligible":true,"supplier":"private"}`, `{"eligible":true}`},
		{`{"eligible":false,"blocker":"supplier_mismatch","tax_id":"private","total":5}`, `{"eligible":false,"blocker":"supplier_mismatch"}`},
		{`{"eligible":false,"blocker":"private"}`, ""},
		{`{"eligible":null,"blocker":"not_reviewed"}`, ""},
		{`{"eligible":false}`, ""},
		{`{"eligible":true,"blocker":"not_reviewed"}`, ""},
		{`{"eligible":"true"}`, ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				const path = "/api/v1/mcp/entities/1/outlets/2/documents/3/payable-preflight"
				if r.Method != http.MethodGet || r.URL.Path != path || r.URL.RawQuery != "" {
					t.Error("request outside expected scope")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.input)
			}))
			defer web.Close()
			client, err := New(web.URL, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.PayablePreflight(t.Context(), 1, 2, 3)
			if tc.want == "" {
				if err == nil {
					t.Fatal("malformed response accepted")
				}
				return
			}
			raw, marshalErr := json.Marshal(result)
			if err != nil || marshalErr != nil || string(raw) != tc.want {
				t.Fatalf("got %s (%v, %v), want %s", raw, err, marshalErr, tc.want)
			}
		})
	}
}
