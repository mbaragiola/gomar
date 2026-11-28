package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

// The existing payable scope denies direct foreign settlements. History also
// fails closed for mixed-outlet splits and reversals (in both directions).
// Legacy allocation-only reversal links need not match the payment graph.
const mcpPayableMovementScope = mcpPayableScope + `
 AND NOT EXISTS (
  WITH touched AS (
   SELECT a.payment_id FROM ap_allocations a WHERE a.payable_id=p.id
   UNION
   SELECT l.payment_id FROM ap_ledger l WHERE l.payable_id=p.id AND l.payment_id IS NOT NULL
  ), related AS (
   SELECT t.payment_id FROM touched t
   UNION
   SELECT x.reversal_of_id FROM touched t JOIN ap_payments x ON x.id=t.payment_id WHERE x.reversal_of_id IS NOT NULL
   UNION
   SELECT x.id FROM touched t JOIN ap_payments x ON x.reversal_of_id=t.payment_id
  ), related_allocations AS (
   SELECT a.id,a.payable_id,a.reverses_id FROM ap_allocations a JOIN related r ON r.payment_id=a.payment_id
  )
  SELECT 1 FROM related r JOIN ap_payments x ON x.id=r.payment_id
  WHERE x.entity_id<>p.entity_id OR x.outlet_id<>p.outlet_id
  UNION ALL
  SELECT 1 FROM related_allocations a JOIN ap_payables q ON q.id=a.payable_id
  WHERE q.entity_id<>p.entity_id OR q.outlet_id<>p.outlet_id
  UNION ALL
  SELECT 1 FROM related r JOIN ap_ledger l ON l.payment_id=r.payment_id JOIN ap_payables q ON q.id=l.payable_id
  WHERE q.entity_id<>p.entity_id OR q.outlet_id<>p.outlet_id
  UNION ALL
  SELECT 1 FROM related_allocations a
  JOIN ap_allocations linked ON linked.id=a.reverses_id OR linked.reverses_id=a.id
  JOIN ap_payables q ON q.id=linked.payable_id
  JOIN ap_payments x ON x.id=linked.payment_id
  WHERE q.entity_id<>p.entity_id OR q.outlet_id<>p.outlet_id OR x.entity_id<>p.entity_id OR x.outlet_id<>p.outlet_id
 )`

func mcpListPayableMovements(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpFinancialHistoryOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	id, err := pathID(r, "payable")
	if err != nil || id <= 0 {
		return nil, missingScopedPayable
	}
	result := mcpclient.PayableMovements{PayableID: id, Movements: []mcpclient.PayableMovement{}}
	ctx := r.Context()
	err = tx.QueryRowContext(ctx, "SELECT p.currency"+mcpPayableMovementScope+` AND p.id=$3`, s.ID, outlet, id).Scan(&result.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missingScopedPayable
	}
	if err != nil {
		return nil, err
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) > 1 {
		return nil, badInput
	}
	var after int64
	if len(q) != 0 {
		if len(q["cursor"]) != 1 {
			return nil, badInput
		}
		after, err = mcpclient.ParsePayableMovementCursor(id, q.Get("cursor"))
		if err != nil {
			return nil, badInput
		}
		var own bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ap_ledger WHERE payable_id=$1 AND id=$2)`, id, after).Scan(&own); err != nil {
			return nil, err
		}
		if !own {
			return nil, badInput
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,occurred_at,delta_minor,balance_minor FROM ap_ledger
 WHERE payable_id=$1 AND id>$2 ORDER BY id LIMIT $3`, id, after, mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		if len(result.Movements) == mcpPageSize {
			result.NextCursor = strconv.FormatInt(id, 10) + "." + strconv.FormatInt(last, 10)
			break
		}
		var item mcpclient.PayableMovement
		var stamp string
		if err := rows.Scan(&last, &item.Kind, &stamp, &item.DeltaMinor, &item.BalanceMinor); err != nil {
			return nil, err
		}
		// Never expose legacy non-date text. Retain the movement in recorded
		// ledger order even if its date is missing or invalid.
		if date, err := mcpDateFromInstant(stamp); err == nil {
			item.Date = &date
		}
		result.Movements = append(result.Movements, item)
	}
	return result, rows.Err()
}
