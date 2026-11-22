package mcpclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPayableMovementsRequireBothMinorUnitsAndPreserveZero(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		valid       bool
	}{
		{"settled", `{"kind":"payment","delta_minor":-50,"balance_minor":0,"reason":"private"}`, true},
		{"missing-balance", `{"kind":"payment","delta_minor":-50}`, false},
		{"null-balance", `{"kind":"payment","delta_minor":-50,"balance_minor":null}`, false},
		{"missing-delta", `{"kind":"payment","balance_minor":0}`, false},
		{"null-delta", `{"kind":"payment","delta_minor":null,"balance_minor":0}`, false},
		{"fractional", `{"kind":"payment","delta_minor":-1.5,"balance_minor":0}`, false},
		{"negative-balance", `{"kind":"payment","delta_minor":-50,"balance_minor":-1}`, false},
		{"bad-date", `{"kind":"payment","date":"invalid","delta_minor":-50,"balance_minor":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/mcp/entities/1/outlets/2/payable-records/3/movements" ||
					r.URL.RawQuery != "cursor=3.4" {
					t.Error("request outside expected scope")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"payable_id":3,"currency":"ARS","movements":[%s]}`, tc.entry)
			}))
			defer web.Close()
			client, err := New(web.URL, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			if err != nil {
				t.Fatal(err)
			}
			cursor := "3.4"
			got, err := client.ListPayableMovements(t.Context(), 1, 2, 3, &cursor)
			if tc.valid {
				if err != nil || len(got.Movements) != 1 || got.Movements[0].BalanceMinor != 0 ||
					got.Movements[0].DeltaMinor != -50 {
					t.Fatal("valid zero balance did not survive")
				}
			} else if err == nil {
				t.Fatal("malformed movement accepted")
			}
		})
	}
}
