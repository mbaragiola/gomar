package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func (a *app) registerMCPFinanceStockReads(add func(string, string, http.HandlerFunc)) {
	add(mcpWorkspaceBase+"/documents/{document}/payable-preflight", http.MethodGet, a.mcpRead(mcpPayablePreflight))
	add(mcpWorkspaceBase+"/payable-records", http.MethodGet, a.mcpRead(mcpListPayableRecords))
	add(mcpWorkspaceBase+"/payable-records/{payable}", http.MethodGet, a.mcpRead(mcpReadPayableRecord))
	add(mcpWorkspaceBase+"/payable-records/{payable}/movements", http.MethodGet, a.mcpRead(mcpListPayableMovements))
	add(mcpWorkspaceBase+"/recorded-payments", http.MethodGet, a.mcpRead(mcpListRecordedPayments))
	add(mcpWorkspaceBase+"/recorded-payments/{payment}", http.MethodGet, a.mcpRead(mcpReadRecordedPayment))
	if a.enablePOS && a.enableStock {
		add(mcpWorkspaceBase+"/stock/products/{product}/movements", http.MethodGet, a.mcpRead(mcpListStockMovementDetails))
	}
}

// Monetary row visibility is intentionally narrower than the browser. Do not
// emit amounts changed by, or bundled into, another outlet's settlements.
// Explicitly test both sides of reversal links and historical ledger links.
const mcpPayableScope = `
 FROM ap_payables p
 WHERE p.entity_id=$1 AND p.outlet_id=$2
 AND NOT EXISTS (
  SELECT 1 FROM ap_allocations a JOIN ap_payments x ON x.id=a.payment_id
  WHERE a.payable_id=p.id AND (x.entity_id<>p.entity_id OR x.outlet_id<>p.outlet_id)
 )
 AND NOT EXISTS (
  SELECT 1 FROM ap_ledger l JOIN ap_payments x ON x.id=l.payment_id
  WHERE l.payable_id=p.id AND (x.entity_id<>p.entity_id OR x.outlet_id<>p.outlet_id)
 )`

const mcpRecordedPaymentScope = `
 FROM ap_payments p
 WHERE p.entity_id=$1 AND p.outlet_id=$2
 AND NOT EXISTS (
  SELECT 1 FROM ap_payments linked WHERE (linked.id=p.reversal_of_id OR linked.reversal_of_id=p.id)
  AND (linked.entity_id<>p.entity_id OR linked.outlet_id<>p.outlet_id)
 )
 AND NOT EXISTS (
  SELECT 1 FROM ap_allocations a JOIN ap_payables q ON q.id=a.payable_id
  WHERE (a.payment_id=p.id OR a.payment_id=p.reversal_of_id OR EXISTS(
   SELECT 1 FROM ap_payments reversal WHERE reversal.id=a.payment_id AND reversal.reversal_of_id=p.id))
  AND (q.entity_id<>p.entity_id OR q.outlet_id<>p.outlet_id)
 )
 AND NOT EXISTS (
  SELECT 1 FROM ap_ledger l JOIN ap_payables q ON q.id=l.payable_id
  WHERE (l.payment_id=p.id OR l.payment_id=p.reversal_of_id OR EXISTS(
   SELECT 1 FROM ap_payments reversal WHERE reversal.id=l.payment_id AND reversal.reversal_of_id=p.id))
  AND (q.entity_id<>p.entity_id OR q.outlet_id<>p.outlet_id)
 )`

const mcpPayableRecordSelect = `SELECT p.id,p.state,p.currency,p.issue_date_snapshot,NULLIF(p.due_date_snapshot,''),p.posted_at,p.original_minor,p.balance_minor`
const mcpRecordedPaymentSelect = `SELECT p.id,CASE WHEN p.kind='reversal' THEN 'posted_reversal' ELSE p.status END,p.currency,p.created_at,p.posted_at,p.total_minor`

func mcpFinanceOutlet(r *http.Request, tx *sql.Tx, s mcpTokenScope) (int64, error) {
	if !slices.Contains(capabilities[s.role], recordPayments) {
		return 0, forbiddenRole
	}
	if err := requireFinancialOutlet(tx, s.entityMembership); err != nil {
		return 0, err
	}
	return mcpWorkspaceOutlet(r, tx, s)
}

var missingScopedPayable = problem{404, "No se encontró la cuenta a pagar."}

type mcpRecordScanner interface{ Scan(...any) error }

func mcpAccountingDate(raw string) bool {
	date, err := time.Parse(time.DateOnly, raw)
	return err == nil && date.Year() >= 1 && date.Format(time.DateOnly) == raw
}

func mcpDateFromInstant(raw string) (string, error) {
	stamp, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || stamp.Year() < 1 {
		return "", errors.New("invalid recorded accounting date")
	}
	date := stamp.In(payablesLocation).Format(time.DateOnly)
	if !mcpAccountingDate(date) {
		return "", errors.New("invalid recorded accounting date")
	}
	return date, nil
}

func mcpScanPayableRecord(row mcpRecordScanner) (mcpclient.PayableRecord, error) {
	var x mcpclient.PayableRecord
	var posted string
	err := row.Scan(&x.ID, &x.Status, &x.Currency, &x.IssueDate, &x.DueDate, &posted, &x.TotalMinor, &x.BalanceMinor)
	if err != nil {
		return x, err
	}
	if !mcpAccountingDate(x.IssueDate) || x.DueDate != nil && !mcpAccountingDate(*x.DueDate) {
		return x, errors.New("invalid payable date")
	}
	x.PostedDate, err = mcpDateFromInstant(posted)
	return x, err
}

func mcpScanRecordedPayment(row mcpRecordScanner) (mcpclient.RecordedPayment, error) {
	var x mcpclient.RecordedPayment
	var created string
	var posted *string
	err := row.Scan(&x.ID, &x.Status, &x.Currency, &created, &posted, &x.TotalMinor)
	if err != nil {
		return x, err
	}
	x.CreatedDate, err = mcpDateFromInstant(created)
	if err != nil {
		return x, err
	}
	if posted != nil {
		date, err := mcpDateFromInstant(*posted)
		if err != nil {
			return x, err
		}
		x.PostedDate = &date
	}
	return x, nil
}

func mcpPayablePage(r *http.Request) (int64, []string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, nil, badInput
	}
	var due []string
	from, to := query["due_from"], query["due_to"]
	if from != nil || to != nil {
		if len(from) != 1 || len(to) != 1 || !mcpAccountingDate(from[0]) || !mcpAccountingDate(to[0]) || from[0] > to[0] {
			return 0, nil, badInput
		}
		due = []string{from[0], to[0]}
		query.Del("due_from")
		query.Del("due_to")
	}
	after, err := mcpAccountingCursorValues(query)
	return after, due, err
}

func mcpFinanceRecords[T any](r *http.Request, tx *sql.Tx, s mcpTokenScope, query string, payableDueFilter bool, scan func(mcpRecordScanner) (T, error), id func(T) int64) (any, error) {
	outlet, err := mcpFinanceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	var after int64
	var due []string
	if payableDueFilter {
		after, due, err = mcpPayablePage(r)
	} else {
		after, err = mcpAccountingCursor(r)
	}
	if err != nil {
		return nil, err
	}
	args := []any{s.ID, outlet, after, mcpPageSize + 1}
	if len(due) != 0 {
		// Match the native civil-date snapshot comparison, before pagination.
		query += ` AND p.due_date_snapshot >= $5 AND p.due_date_snapshot <= $6`
		args = append(args, due[0], due[1])
	}
	rows, err := tx.QueryContext(r.Context(), query+` AND p.id>$3 ORDER BY p.id LIMIT $4`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := mcpclient.FinanceRecords[T]{Records: []T{}}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result.Records = append(result.Records, item)
	}
	if len(result.Records) > mcpPageSize {
		result.Records = result.Records[:mcpPageSize]
		result.NextID = id(result.Records[mcpPageSize-1])
	}
	return result, rows.Err()
}

func mcpFinanceRecord[T any](r *http.Request, tx *sql.Tx, s mcpTokenScope, query, pathKey string, missing error, scan func(mcpRecordScanner) (T, error)) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	outlet, err := mcpFinanceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	id, err := pathID(r, pathKey)
	if err != nil || id <= 0 {
		return nil, missing
	}
	result, err := scan(tx.QueryRowContext(r.Context(), query+` AND p.id=$3`, s.ID, outlet, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missing
	}
	return result, err
}

func mcpListPayableRecords(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	return mcpFinanceRecords(r, tx, s, mcpPayableRecordSelect+mcpPayableScope, true, mcpScanPayableRecord, func(x mcpclient.PayableRecord) int64 { return x.ID })
}
func mcpReadPayableRecord(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	return mcpFinanceRecord(r, tx, s, mcpPayableRecordSelect+mcpPayableScope, "payable", missingScopedPayable, mcpScanPayableRecord)
}
func mcpListRecordedPayments(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	return mcpFinanceRecords(r, tx, s, mcpRecordedPaymentSelect+mcpRecordedPaymentScope, false, mcpScanRecordedPayment, func(x mcpclient.RecordedPayment) int64 { return x.ID })
}
func mcpReadRecordedPayment(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	return mcpFinanceRecord(r, tx, s, mcpRecordedPaymentSelect+mcpRecordedPaymentScope, "payment", missingPayment, mcpScanRecordedPayment)
}

func mcpListStockMovementDetails(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if !slices.Contains([]string{"administration", "purchasing", "accounting"}, s.role) {
		return nil, forbiddenRole
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	product, err := pathID(r, "product")
	if err != nil || product <= 0 {
		return nil, badInput
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["start_date"]) != 1 || len(q["days"]) != 1 {
		return nil, badInput
	}
	var after int64
	for key, values := range q {
		if len(values) != 1 {
			return nil, badInput
		}
		switch key {
		case "start_date", "days":
		case "after_id":
			after, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || after <= 0 {
				return nil, badInput
			}
		default:
			return nil, badInput
		}
	}
	start, err := reservationDay(q.Get("start_date"))
	if err != nil {
		return nil, err
	}
	days, err := strconv.Atoi(q.Get("days"))
	if err != nil || days < 1 || days > 31 {
		return nil, badInput
	}
	end := start.AddDate(0, 0, days)
	var tracked bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM stock_products WHERE outlet_id=$1 AND product_id=$2)`, outlet, product).Scan(&tracked); err != nil {
		return nil, err
	}
	if !tracked {
		return nil, posMissingProduct
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,kind,delta,recorded_at FROM stock_movements
 WHERE outlet_id=$1 AND product_id=$2 AND id>$3 AND recorded_at >= $4 AND recorded_at < $5 ORDER BY id LIMIT $6`,
		outlet, product, after, start.UTC().Format(utcSecondPrefix), end.UTC().Format(utcSecondPrefix), mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := mcpclient.StockMovementDetails{Timezone: reservationTimezone, StartDate: start.Format(time.DateOnly), EndDateExclusive: end.Format(time.DateOnly), Movements: []mcpclient.StockMovementDetail{}}
	var last int64
	for rows.Next() {
		var id int64
		var item mcpclient.StockMovementDetail
		if err := rows.Scan(&id, &item.Kind, &item.Delta, &item.RecordedAt); err != nil {
			return nil, err
		}
		if len(result.Movements) == mcpPageSize {
			result.NextID = last
			break
		}
		stamp, err := time.Parse(time.RFC3339Nano, item.RecordedAt)
		if err != nil {
			return nil, errors.New("invalid stock movement date")
		}
		item.RecordedAt = stamp.In(reservationLocation).Format(time.RFC3339Nano)
		result.Movements = append(result.Movements, item)
		last = id
	}
	return result, rows.Err()
}
