package main

import (
	"fmt"
	"time"
	"unicode/utf8"
)

type reconciliationReferenceRow struct {
	ID        int64  `json:"id"`
	Side      string `json:"side"`
	Outlet    int64  `json:"outlet"`
	Date      string `json:"date"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Reference string `json:"reference"`
}

type reconciliationReferenceGroup struct {
	Kind     string  `json:"kind"`
	Payments []int64 `json:"payments"`
	CashFlow []int64 `json:"cash_flow"`
}

type reconciliationReferenceKey struct {
	outlet              int64
	date                string
	amount              int64
	currency, reference string
	// Missing references never match, even across otherwise identical rows.
	// Nonempty references leave this at zero; the blank-row index is one based.
	missingIndex int
}

// buildExactReferenceGroups classifies a bounded ordered slice of normalized
// rows. It accepts no scope/eligibility decision from a caller and performs
// no reconciliation or write; callers must scope the input before use.
func buildExactReferenceGroups(rows []reconciliationReferenceRow) ([]reconciliationReferenceGroup, error) {
	return buildReconciliationGroups(rows, true)
}

// buildNonReferenceGroups retains the same bounded one-to-one, ambiguous,
// and unmatched shapes while intentionally ignoring reference values.
func buildNonReferenceGroups(rows []reconciliationReferenceRow) ([]reconciliationReferenceGroup, error) {
	return buildReconciliationGroups(rows, false)
}

func buildReconciliationGroups(rows []reconciliationReferenceRow, matchReference bool) ([]reconciliationReferenceGroup, error) {
	if len(rows) > 20000 {
		return nil, fmt.Errorf("too many normalized reconciliation rows")
	}
	groups := []reconciliationReferenceGroup{}
	keys := make(map[reconciliationReferenceKey]int)
	seen := make(map[int64]bool)
	for i, row := range rows {
		date, err := time.Parse(time.DateOnly, row.Date)
		if row.ID < 1 || seen[row.ID] || row.Outlet < 1 || err != nil || date.Year() < 1 ||
			(row.Side != "payments" && row.Side != "cash_flow") ||
			(row.Currency != "ARS" && row.Currency != "USD") ||
			row.Amount < -99999999999999 || row.Amount > 99999999999999 ||
			!utf8.ValidString(row.Reference) || utf8.RuneCountInString(row.Reference) > 1024 {
			return nil, fmt.Errorf("unsupported normalized reconciliation row at index %d", i)
		}
		seen[row.ID] = true
		key := reconciliationReferenceKey{outlet: row.Outlet, date: row.Date, amount: row.Amount,
			currency: row.Currency}
		if matchReference {
			key.reference = row.Reference
			if row.Reference == "" {
				key.missingIndex = i + 1
			}
		}
		group, exists := keys[key]
		if !exists {
			group = len(groups)
			keys[key] = group
			groups = append(groups, reconciliationReferenceGroup{Payments: []int64{}, CashFlow: []int64{}})
		}
		dst := &groups[group]
		if row.Side == "payments" {
			dst.Payments = append(dst.Payments, row.ID)
		} else {
			dst.CashFlow = append(dst.CashFlow, row.ID)
		}
	}
	for i := range groups {
		group := &groups[i]
		switch {
		case len(group.Payments) == 1 && len(group.CashFlow) == 1:
			group.Kind = "candidate"
		case len(group.Payments) > 0 && len(group.CashFlow) > 0:
			group.Kind = "ambiguous"
		default:
			group.Kind = "unmatched"
		}
	}
	return groups, nil
}
