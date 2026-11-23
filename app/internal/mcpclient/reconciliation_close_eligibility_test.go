package mcpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReconciliationCloseEligibilitySanitizesFixedResponse(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`{"eligible":true,"rows":["private"]}`, `{"eligible":true}`},
		{`{"eligible":false,"blocker":"not_in_review","bank":"private"}`, `{"eligible":false,"blocker":"not_in_review"}`},
		{`{"eligible":false,"blocker":"unsupported_review_data","row_count":5}`, `{"eligible":false,"blocker":"unsupported_review_data"}`},
		{`{"eligible":true,"handoff_url":"/#conciliacion=1:2:3"}`, `{"eligible":true,"handoff_url":"/#conciliacion=1:2:3"}`},
		{`{"eligible":false,"blocker":"unsupported_review_data","handoff_url":"/#conciliacion=1:2:3"}`, `{"eligible":false,"blocker":"unsupported_review_data","handoff_url":"/#conciliacion=1:2:3"}`},
		{`{"eligible":false,"blocker":"not_in_review","handoff_url":"/#conciliacion=1:2:3"}`, ""},
		{`{"eligible":true,"handoff_url":"https://example.invalid/#conciliacion=1:2:3"}`, ""},
		{`{"eligible":true,"handoff_url":"/#conciliacion=1:2:4"}`, ""},
		{`{"eligible":true,"handoff_url":"/?revision=3#conciliacion=1:2:3"}`, ""},
		{`{"eligible":false,"blocker":"pending_rows"}`, ""},
		{`{"eligible":false}`, ""},
		{`{"eligible":true,"blocker":"not_in_review"}`, ""},
		{`{"eligible":null,"blocker":"not_in_review"}`, ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				const path = "/api/v1/mcp/entities/1/outlets/2/reconciliation-runs/3/close-eligibility"
				if r.Method != http.MethodGet || r.URL.Path != path || r.URL.RawQuery != "" {
					t.Error("request outside exact scope")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.input)
			}))
			defer web.Close()
			client, err := New(web.URL, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.ReadReconciliationCloseEligibility(t.Context(), 1, 2, 3)
			if tc.want == "" {
				if err == nil {
					t.Fatal("malformed response accepted")
				}
				return
			}
			raw, merr := json.Marshal(result)
			if err != nil || merr != nil || string(raw) != tc.want {
				t.Fatalf("got %s (%v, %v), want %s", raw, err, merr, tc.want)
			}
		})
	}
}
