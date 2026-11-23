package mcpclient

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
	_ "time/tzdata"
)

type FinanceRecords[T any] struct {
	Records []T   `json:"records"`
	NextID  int64 `json:"next_id,omitempty"`
}
type PayableRecord struct {
	ID           int64   `json:"id"`
	Status       string  `json:"status"`
	Currency     string  `json:"currency"`
	IssueDate    string  `json:"issue_date"`
	DueDate      *string `json:"due_date,omitempty"`
	PostedDate   string  `json:"posted_date"`
	TotalMinor   int64   `json:"total_minor"`
	BalanceMinor int64   `json:"balance_minor"`
}
type RecordedPayment struct {
	ID          int64   `json:"id"`
	Status      string  `json:"status"`
	Currency    string  `json:"currency"`
	CreatedDate string  `json:"created_date"`
	PostedDate  *string `json:"posted_date,omitempty"`
	TotalMinor  int64   `json:"total_minor"`
}
type StockMovementDetail struct {
	Kind       string `json:"kind"`
	Delta      int64  `json:"delta"`
	RecordedAt string `json:"recorded_at"`
}
type StockMovementDetails struct {
	Timezone         string                `json:"timezone"`
	StartDate        string                `json:"start_date"`
	EndDateExclusive string                `json:"end_date_exclusive"`
	Movements        []StockMovementDetail `json:"movements"`
	NextID           int64                 `json:"next_id,omitempty"`
}

func validFinanceCurrency(currency string) bool { return currency == "ARS" || currency == "USD" }

func validPayable(x PayableRecord) bool {
	if x.ID <= 0 || !validFinanceCurrency(x.Currency) || !accountingDate(x.IssueDate) || !accountingDate(x.PostedDate) ||
		x.DueDate != nil && !accountingDate(*x.DueDate) || x.TotalMinor <= 0 || x.BalanceMinor < 0 || x.BalanceMinor > x.TotalMinor {
		return false
	}
	switch x.Status {
	case "open":
		return x.BalanceMinor == x.TotalMinor
	case "partial":
		return x.BalanceMinor > 0 && x.BalanceMinor < x.TotalMinor
	case "settled":
		return x.BalanceMinor == 0
	default:
		return false
	}
}
func validRecordedPayment(x RecordedPayment) bool {
	if x.ID <= 0 || !validFinanceCurrency(x.Currency) || !accountingDate(x.CreatedDate) || x.TotalMinor <= 0 {
		return false
	}
	switch x.Status {
	case "draft":
		return x.PostedDate == nil
	case "posted", "reversed", "posted_reversal":
		return x.PostedDate != nil && accountingDate(*x.PostedDate)
	default:
		return false
	}
}

func listFinanceRecords[T any](ctx context.Context, c *Client, entity, outlet int64, after *int64, extra url.Values, path string, valid func(T) bool, id func(T) int64) (FinanceRecords[T], error) {
	var result FinanceRecords[T]
	q, last, err := accountingQuery(after)
	if err != nil {
		return result, err
	}
	if extra != nil {
		if q == nil {
			q = url.Values{}
		}
		for key, values := range extra {
			q[key] = values
		}
	}
	if err := c.getWorkspace(ctx, entity, outlet, path, q, &result); err != nil {
		return FinanceRecords[T]{}, err
	}
	for _, row := range result.Records {
		if !valid(row) || id(row) <= last {
			return FinanceRecords[T]{}, invalidResponse
		}
		last = id(row)
	}
	if result.Records == nil || !validAccountingPage(result.NextID, last, len(result.Records)) {
		return FinanceRecords[T]{}, invalidResponse
	}
	return result, nil
}

func readFinanceRecord[T any](ctx context.Context, c *Client, entity, outlet, id int64, path, suggestion string, valid func(T) bool, recordID func(T) int64) (T, error) {
	var result T
	if id <= 0 {
		return result, errors.New(suggestion)
	}
	if err := c.getWorkspace(ctx, entity, outlet, path+"/"+strconv.FormatInt(id, 10), nil, &result); err != nil {
		var empty T
		return empty, err
	}
	if !valid(result) || recordID(result) != id {
		var empty T
		return empty, invalidResponse
	}
	return result, nil
}

func (c *Client) ListPayables(ctx context.Context, entity, outlet int64, after *int64) (FinanceRecords[PayableRecord], error) {
	return listFinanceRecords(ctx, c, entity, outlet, after, nil, "/payable-records", validPayable, func(p PayableRecord) int64 { return p.ID })
}

// ListPayablesDue filters by an inclusive civil-date range. Keep both dates
// when continuing with next_id: each page reads current records.
func (c *Client) ListPayablesDue(ctx context.Context, entity, outlet int64, after *int64, from, to string) (FinanceRecords[PayableRecord], error) {
	for _, raw := range []string{from, to} {
		date, err := time.Parse(time.DateOnly, raw)
		if err != nil || date.Year() < 1 || date.Format(time.DateOnly) != raw {
			return FinanceRecords[PayableRecord]{}, errors.New("indicá due_from y due_to como fechas YYYY-MM-DD")
		}
	}
	if from > to {
		return FinanceRecords[PayableRecord]{}, errors.New("due_from no puede ser posterior a due_to")
	}
	return listFinanceRecords(ctx, c, entity, outlet, after, url.Values{"due_from": {from}, "due_to": {to}}, "/payable-records", func(p PayableRecord) bool {
		return validPayable(p) && p.DueDate != nil && *p.DueDate >= from && *p.DueDate <= to
	}, func(p PayableRecord) int64 { return p.ID })
}

func (c *Client) ReadPayable(ctx context.Context, entity, outlet, id int64) (PayableRecord, error) {
	return readFinanceRecord(ctx, c, entity, outlet, id, "/payable-records", "indicá el payable_id devuelto por gomar_list_payables", validPayable, func(p PayableRecord) int64 { return p.ID })
}
func (c *Client) ListRecordedPayments(ctx context.Context, entity, outlet int64, after *int64) (FinanceRecords[RecordedPayment], error) {
	return listFinanceRecords(ctx, c, entity, outlet, after, nil, "/recorded-payments", validRecordedPayment, func(p RecordedPayment) int64 { return p.ID })
}
func (c *Client) ReadRecordedPayment(ctx context.Context, entity, outlet, id int64) (RecordedPayment, error) {
	return readFinanceRecord(ctx, c, entity, outlet, id, "/recorded-payments", "indicá el payment_id devuelto por gomar_list_recorded_payments", validRecordedPayment, func(p RecordedPayment) int64 { return p.ID })
}

func (c *Client) ListStockMovements(ctx context.Context, entity, outlet, product int64, startDate string, days int, after *int64) (StockMovementDetails, error) {
	var result StockMovementDetails
	start, err := parseReportDate(startDate)
	if err != nil {
		return result, err
	}
	if product <= 0 || days < 1 || days > 31 {
		return result, errors.New("indicá un product_id de la sucursal y un período de 1 a 31 días")
	}
	q, cursor, err := accountingQuery(after)
	if err != nil {
		return result, err
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("start_date", startDate)
	q.Set("days", strconv.Itoa(days))
	if err := c.getWorkspace(ctx, entity, outlet, "/stock/products/"+strconv.FormatInt(product, 10)+"/movements", q, &result); err != nil {
		return StockMovementDetails{}, err
	}
	endDate := start.AddDate(0, 0, days).Format(time.DateOnly)
	if result.Timezone != buenosAires || result.StartDate != startDate || result.EndDateExclusive != endDate ||
		result.Movements == nil || len(result.Movements) > workspacePageSize ||
		result.NextID != 0 && (result.NextID <= cursor || len(result.Movements) != workspacePageSize) {
		return StockMovementDetails{}, invalidResponse
	}
	local, err := time.LoadLocation(buenosAires)
	if err != nil {
		return StockMovementDetails{}, invalidResponse
	}
	for _, movement := range result.Movements {
		switch movement.Kind {
		case "recepcion", "merma", "conteo", "venta":
		default:
			return StockMovementDetails{}, invalidResponse
		}
		stamp, err := time.Parse(time.RFC3339Nano, movement.RecordedAt)
		if err != nil {
			return StockMovementDetails{}, invalidResponse
		}
		date := stamp.In(local).Format(time.DateOnly)
		if date < startDate || date >= endDate {
			return StockMovementDetails{}, invalidResponse
		}
	}
	return result, nil
}
