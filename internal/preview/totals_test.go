package preview

import (
	"math"
	"reflect"
	"testing"
)

func TestDemoTotals(t *testing.T) {
	got, err := demoSummary()
	want := summary{true, "2030-04-08", []currencyTotals{{"ARS", 3, 20950, 1, 12850}, {"USD", 1, 625, 0, 0}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("demo = %#v, %v; want %#v", got, err, want)
	}
}

func TestInvalidInvoiceInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []invoice
	}{
		{"duplicate", []invoice{{1, "ARS", 100, ""}, {1, "USD", 0, ""}}},
		{"negative balance", []invoice{{1, "ARS", -1, ""}}},
		{"unsupported currency", []invoice{{1, "EUR", 1, ""}}},
		{"invalid calendar date", []invoice{{1, "USD", 1, "2030-02-30"}}},
		{"int64 overflow", []invoice{{1, "ARS", math.MaxInt64, ""}, {2, "ARS", 1, ""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := summarize("2030-04-08", tc.rows)
			if err == nil || got.Totals != nil {
				t.Fatal("invalid input returned results")
			}
		})
	}
}
