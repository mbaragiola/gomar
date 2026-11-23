// Package tablecheck validates untendered POS table checks. It does not
// record sales, tenders, stock movements, or fiscal documents.
package tablecheck

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	MaxTable      = 9999
	MaxLines      = 80
	MaxQuantity   = 999
	MaxUnitMinor  = 100_000_000
	MaxTotalMinor = 10_000_000_000
)

var (
	ErrIntent  = errors.New("invalid table check")
	ErrCatalog = errors.New("items do not match the current outlet catalog")
	requestID  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type Item struct {
	ProductID int64
	Revision  int64
	Quantity  int64
}

// Intent is immutable within this package. Only inputs validated by NewIntent
// should reach a store. Actor and outlet belong to the authenticated store
// scope, never to an untrusted body field.
type Intent struct {
	id    string
	table int64
	items []Item
}

func NewIntent(id string, table int64, items []Item) (Intent, error) {
	if !requestID.MatchString(id) || table < 1 || table > MaxTable || len(items) == 0 || len(items) > MaxLines {
		return Intent{}, ErrIntent
	}
	lines := slices.Clone(items)
	slices.SortFunc(lines, func(a, b Item) int {
		switch {
		case a.ProductID < b.ProductID:
			return -1
		case a.ProductID > b.ProductID:
			return 1
		default:
			return 0
		}
	})
	var last int64
	for _, i := range lines {
		if i.ProductID <= last || i.Revision < 1 || i.Quantity < 1 || i.Quantity > MaxQuantity {
			return Intent{}, ErrIntent
		}
		last = i.ProductID
	}
	return Intent{id: id, table: table, items: lines}, nil
}

func (i Intent) ID() string    { return i.id }
func (i Intent) Table() int64  { return i.table }
func (i Intent) Items() []Item { return slices.Clone(i.items) }

// Fingerprint is for replay comparison after the store looks up the exact
// authenticated outlet, actor, and request ID. It does not grant access.
func (i Intent) Fingerprint() [sha256.Size]byte {
	data := []byte("gomar-pos-table-check:v1\x00")
	data = binary.BigEndian.AppendUint64(data, uint64(i.table))
	for _, item := range i.items {
		data = binary.BigEndian.AppendUint64(data, uint64(item.ProductID))
		data = binary.BigEndian.AppendUint64(data, uint64(item.Revision))
		data = binary.BigEndian.AppendUint64(data, uint64(item.Quantity))
	}
	return sha256.Sum256(data)
}

type CatalogItem struct {
	ProductID int64
	OutletID  int64
	Revision  int64
	Name      string
	UnitMinor int64
	Active    bool
}

type Line struct {
	Item
	Name      string
	UnitMinor int64
}

// Snapshot uses only the exact requested active revisions in the authorized
// outlet's current catalog. Reopening a saved check should read its frozen
// lines from the store, not use this function to reprice them.
func Snapshot(i Intent, outlet int64, current []CatalogItem) ([]Line, int64, error) {
	if i.id == "" || len(i.items) == 0 || outlet < 1 || len(current) != len(i.items) {
		return nil, 0, ErrCatalog
	}
	byID := make(map[int64]CatalogItem, len(current))
	for _, c := range current {
		if c.ProductID < 1 || c.OutletID != outlet || !c.Active || c.Revision < 1 ||
			c.UnitMinor < 1 || c.UnitMinor > MaxUnitMinor || !utf8.ValidString(c.Name) ||
			strings.TrimSpace(c.Name) == "" || utf8.RuneCountInString(c.Name) > 120 ||
			strings.ContainsRune(c.Name, 0) {
			return nil, 0, ErrCatalog
		}
		if _, duplicate := byID[c.ProductID]; duplicate {
			return nil, 0, ErrCatalog
		}
		byID[c.ProductID] = c
	}
	var total int64
	lines := make([]Line, 0, len(i.items))
	for _, item := range i.items {
		c, ok := byID[item.ProductID]
		if !ok || c.Revision != item.Revision {
			return nil, 0, ErrCatalog
		}
		total += c.UnitMinor * item.Quantity
		if total > MaxTotalMinor {
			return nil, 0, ErrCatalog
		}
		lines = append(lines, Line{Item: item, Name: c.Name, UnitMinor: c.UnitMinor})
	}
	return lines, total, nil
}
