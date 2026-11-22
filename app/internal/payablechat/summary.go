// Package payablechat defines the read contract for chat adapters.
package payablechat

import (
	"context"
	"errors"
)

const asOf = "2028-03-01"

type Reader interface {
	Summary(context.Context) (Summary, error)
}

type syntheticReader struct {
	scope string
	rows  []payable
}

// NewSynthetic binds a generated scope at startup. Callers cannot change it.
func NewSynthetic(scope string) (Reader, error) {
	rows, ok := fixture(scope)
	if !ok {
		return nil, errors.New("unknown synthetic scope")
	}
	return &syntheticReader{scope, rows}, nil
}

func (r *syntheticReader) Summary(ctx context.Context) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	return summarize(r.scope, r.rows), nil
}

type payable struct {
	currency string
	cents    int64
	due      string
}

func fixture(scope string) ([]payable, bool) {
	switch scope {
	case "centro":
		return []payable{{"ARS", 12500, "2028-02-29"}, {"ARS", 2500, asOf}, {"ARS", 1000, ""},
			{"ARS", 0, "2028-02-28"}, {"USD", 450, "2028-02-29"}}, true
	case "norte":
		return []payable{{"ARS", 7100, "2028-03-02"}}, true
	default:
		return nil, false
	}
}

type Totals struct {
	Currency         string `json:"moneda"`
	OutstandingCount int    `json:"pendientes"`
	OutstandingCents int64  `json:"saldo_centavos"`
	OverdueCount     int    `json:"vencidas"`
	OverdueCents     int64  `json:"vencido_centavos"`
}

type Summary struct {
	Synthetic bool     `json:"datos_sinteticos"`
	Scope     string   `json:"alcance"`
	AsOf      string   `json:"fecha_argentina"`
	Totals    []Totals `json:"totales"`
}

func summarize(scope string, rows []payable) Summary {
	result := Summary{true, scope, asOf, []Totals{}}
	for _, currency := range []string{"ARS", "USD"} {
		bucket := Totals{Currency: currency}
		for _, p := range rows {
			if p.currency != currency || p.cents <= 0 {
				continue
			}
			bucket.OutstandingCount++
			bucket.OutstandingCents += p.cents
			if p.due != "" && p.due < asOf {
				bucket.OverdueCount++
				bucket.OverdueCents += p.cents
			}
		}
		if bucket.OutstandingCount > 0 {
			result.Totals = append(result.Totals, bucket)
		}
	}
	return result
}
