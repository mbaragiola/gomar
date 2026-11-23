package tablecheck

import (
	"errors"
	"reflect"
	"testing"
)

const request = "00112233-4455-4677-8899-aabbccddeeff"

func mustIntent(t *testing.T, table int64, items []Item) Intent {
	t.Helper()
	i, err := NewIntent(request, table, items)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestCanonicalFingerprintAndCopies(t *testing.T) {
	input := []Item{{ProductID: 2, Revision: 3, Quantity: 1}, {ProductID: 1, Revision: 1, Quantity: 2}}
	i := mustIntent(t, 21, input)
	r := mustIntent(t, 21, []Item{input[1], input[0]})
	if i.ID() != request || i.Table() != 21 || i.Fingerprint() != r.Fingerprint() {
		t.Fatal("same replay lost identity")
	}
	if input[0].ProductID != 2 {
		t.Fatal("input re-ordered in place")
	}
	input[0].Revision = 13
	copied := i.Items()
	copied[0].Quantity = 59
	if i.Fingerprint() != r.Fingerprint() || i.Items()[0].Quantity != 2 {
		t.Fatal("caller changed the normalized intent")
	}
	for _, different := range []Intent{
		mustIntent(t, 22, r.Items()),
		mustIntent(t, 21, []Item{{ProductID: 2, Revision: 4, Quantity: 1}, {ProductID: 1, Revision: 1, Quantity: 2}}),
		mustIntent(t, 21, []Item{{ProductID: 2, Revision: 3, Quantity: 2}, {ProductID: 1, Revision: 1, Quantity: 2}}),
	} {
		if i.Fingerprint() == different.Fingerprint() {
			t.Fatal("changed input matched an existing request")
		}
	}
}

func TestIntentLimits(t *testing.T) {
	good := []Item{{ProductID: 1, Revision: 1, Quantity: 1}}
	cases := []struct {
		id    string
		table int64
		items []Item
	}{
		{"", 1, good}, {request, 0, good}, {request, MaxTable + 1, good},
		{request, 1, nil}, {request, 1, make([]Item, MaxLines+1)},
		{request, 1, []Item{{1, 1, 1}, {1, 2, 3}}},
		{request, 1, []Item{{1, 0, 1}}}, {request, 1, []Item{{1, 1, MaxQuantity + 1}}},
	}
	for _, c := range cases {
		if _, err := NewIntent(c.id, c.table, c.items); !errors.Is(err, ErrIntent) {
			t.Fatalf("accepted invalid intent %+v", c)
		}
	}
}

func TestSnapshotRejectsWrongOutletChangedRevisionAndPrice(t *testing.T) {
	i := mustIntent(t, 1, []Item{{1, 1, 2}, {2, 3, 1}})
	cat := []CatalogItem{{2, 11, 3, "Jugo", 2509, true}, {1, 11, 1, "Café", 1795, true}}
	lines, total, err := Snapshot(i, 11, cat)
	want := []Line{{Item{1, 1, 2}, "Café", 1795}, {Item{2, 3, 1}, "Jugo", 2509}}
	if err != nil || total != 6099 || !reflect.DeepEqual(lines, want) {
		t.Fatalf("snapshot = %v, %d, %v", lines, total, err)
	}
	for name, change := range map[string]func([]CatalogItem){
		"foreign branch":  func(c []CatalogItem) { c[0].OutletID++ },
		"stale revision":  func(c []CatalogItem) { c[0].Revision++ },
		"inactive":        func(c []CatalogItem) { c[0].Active = false },
		"duplicate":       func(c []CatalogItem) { c[0] = c[1] },
		"malformed price": func(c []CatalogItem) { c[0].UnitMinor = MaxUnitMinor + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			s := append([]CatalogItem(nil), cat...)
			change(s)
			if lines, n, err := Snapshot(i, 11, s); !errors.Is(err, ErrCatalog) || lines != nil || n != 0 {
				t.Fatalf("partial or wrong snapshot %v, %d, %v", lines, n, err)
			}
		})
	}
}

func TestSnapshotLimitWithoutPartialResult(t *testing.T) {
	i := mustIntent(t, 1, []Item{{1, 1, 100}, {2, 1, 1}})
	catalog := []CatalogItem{{1, 11, 1, "A", MaxUnitMinor, true}, {2, 11, 1, "B", 1, true}}
	if lines, n, err := Snapshot(i, 11, catalog); !errors.Is(err, ErrCatalog) || lines != nil || n != 0 {
		t.Fatalf("over-limit snapshot %v, %d, %v", lines, n, err)
	}
}
