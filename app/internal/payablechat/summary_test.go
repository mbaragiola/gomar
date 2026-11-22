package payablechat

import (
	"context"
	"reflect"
	"testing"
)

func TestSyntheticSummary(t *testing.T) {
	for _, tc := range []struct {
		scope string
		want  []Totals
	}{
		{"centro", []Totals{{"ARS", 3, 16000, 1, 12500}, {"USD", 1, 450, 1, 450}}},
		{"norte", []Totals{{"ARS", 1, 7100, 0, 0}}},
	} {
		reader, err := NewSynthetic(tc.scope)
		if err != nil {
			t.Fatal(err)
		}
		got, err := reader.Summary(context.Background())
		if err != nil || got.Scope != tc.scope || !got.Synthetic || got.AsOf != "2028-03-01" || !reflect.DeepEqual(got.Totals, tc.want) {
			t.Fatalf("summary %q = %+v, %v", tc.scope, got, err)
		}
	}
	if _, err := NewSynthetic("unconfigured"); err == nil {
		t.Fatal("unknown scope accepted")
	}
}
