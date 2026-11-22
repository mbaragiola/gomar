package preview

import (
	"errors"
	"math"
	"slices"
	"time"
)

const dateLayout = "2006-01-02"

type invoice struct {
	id       int
	currency string
	balance  int64
	due      string
}

type currencyTotals struct {
	Currency       string `json:"moneda"`
	Pending        int    `json:"pendientes"`
	Balance        int64  `json:"saldo_centavos"`
	Overdue        int    `json:"vencidas"`
	OverdueBalance int64  `json:"vencido_centavos"`
}

type summary struct {
	Synthetic bool             `json:"datos_sinteticos"`
	AsOf      string           `json:"fecha_del_ejemplo"`
	Totals    []currencyTotals `json:"totales"`
}

func demoSummary() (summary, error) {
	return summarize("2030-04-08", []invoice{
		{1, "ARS", 12850, "2030-04-06"},
		{2, "ARS", 7100, "2030-04-08"},
		{3, "ARS", 1000, ""},
		{4, "ARS", 0, "2030-04-01"},
		{5, "USD", 625, "2030-04-09"},
	})
}

func summarize(asOf string, rows []invoice) (summary, error) {
	if _, err := time.Parse(dateLayout, asOf); err != nil {
		return summary{}, errors.New("invalid preview date")
	}
	out := summary{Synthetic: true, AsOf: asOf, Totals: []currencyTotals{}}
	seen := make(map[int]bool)
	buckets := make(map[string]currencyTotals)
	for _, row := range rows {
		if row.id <= 0 || seen[row.id] || row.balance < 0 || (row.currency != "ARS" && row.currency != "USD") {
			return summary{}, errors.New("invalid preview invoice")
		}
		seen[row.id] = true
		if row.due != "" {
			if _, err := time.Parse(dateLayout, row.due); err != nil {
				return summary{}, errors.New("invalid preview due date")
			}
		}
		if row.balance == 0 {
			continue
		}
		t := buckets[row.currency]
		if row.balance > math.MaxInt64-t.Balance {
			return summary{}, errors.New("preview total exceeds integer range")
		}
		t.Currency = row.currency
		t.Pending++
		t.Balance += row.balance
		if row.due != "" && row.due < asOf {
			t.Overdue++
			t.OverdueBalance += row.balance
		}
		buckets[row.currency] = t
	}
	for _, value := range buckets {
		out.Totals = append(out.Totals, value)
	}
	slices.SortFunc(out.Totals, func(a, b currencyTotals) int {
		if a.Currency < b.Currency {
			return -1
		}
		if a.Currency > b.Currency {
			return 1
		}
		return 0
	})
	return out, nil
}

// DemoSummary returns the same generated totals used by the Codex preview.
func DemoSummary() (summary, error) {
	return demoSummary()
}
