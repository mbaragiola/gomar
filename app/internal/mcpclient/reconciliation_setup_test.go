package mcpclient

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type reconciliationSetupRoundTrip func(*http.Request) (*http.Response, error)

func (f reconciliationSetupRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReconciliationSetupWire(t *testing.T) {
	const key = "LAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	c, err := New("https://example.com", key)
	if err != nil {
		t.Fatal(err)
	}
	const valid = `{"id":5,"revision":2,"status":"draft","match_reference":false,"has_payments_source":true,"has_cash_flow_source":false,"reference_mapping_incomplete":false,"unsupported_source_setup":false,"over_row_limit":false}`
	body := valid
	c.http.Transport = reconciliationSetupRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/mcp/entities/3/outlets/2/reconciliation-runs/5/setup" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatal("unexpected setup route")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	out, err := c.ReadReconciliationSetup(t.Context(), 3, 2, 5)
	if err != nil || out.ID != 5 || !out.HasPaymentsSource || out.HasCashFlowSource {
		t.Fatal(out, err)
	}
	for _, invalid := range []string{
		strings.Replace(valid, `,"over_row_limit":false`, "", 1),
		strings.Replace(valid, `"has_cash_flow_source":false`, `"has_cash_flow_source":null`, 1),
		strings.Replace(valid, `"id":5`, `"id":6`, 1),
		strings.Replace(valid, `"reference_mapping_incomplete":false`, `"reference_mapping_incomplete":true`, 1),
	} {
		body = invalid
		if _, err := c.ReadReconciliationSetup(t.Context(), 3, 2, 5); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}
