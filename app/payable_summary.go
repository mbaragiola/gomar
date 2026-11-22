package main

import (
	"context"
	"database/sql"
)

type outstandingPayableTotals struct {
	Currency                                                       string
	OutstandingCount, OutstandingMinor, OverdueCount, OverdueMinor int64
}

type outstandingPayableSummary struct {
	AsOf   string
	Totals []outstandingPayableTotals
}

// summarizeOutstandingPayables is an internal read for an already verified
// app actor and explicitly selected outlet. It does not resolve chat identity.
func (a *app) summarizeOutstandingPayables(ctx context.Context, actor user, entityID, outletID int64) (outstandingPayableSummary, error) {
	if actor.id == 0 {
		return outstandingPayableSummary{}, unauthenticated
	}
	if outletID <= 0 {
		return outstandingPayableSummary{}, badInput
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return outstandingPayableSummary{}, err
	}
	defer tx.Rollback()
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND active=TRUE)`, actor.id).Scan(&active); err != nil {
		return outstandingPayableSummary{}, err
	}
	if !active {
		return outstandingPayableSummary{}, unauthenticated
	}
	member, err := scopedMembership(tx, actor.id, entityID, recordPayments)
	if err != nil {
		return outstandingPayableSummary{}, err
	}
	if err := requireFinancialOutlet(tx, member); err != nil {
		return outstandingPayableSummary{}, err
	}
	if err := requireSelectedDocumentOutlet(tx, member, outletID); err != nil {
		return outstandingPayableSummary{}, err
	}
	today := a.payableNow().In(payablesLocation).Format(payablesDate)
	totals, err := queryOutstandingPayableTotals(ctx, tx, member, outletID, today)
	if err != nil {
		return outstandingPayableSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return outstandingPayableSummary{}, err
	}
	return outstandingPayableSummary{AsOf: today, Totals: totals}, nil
}

func queryOutstandingPayableTotals(ctx context.Context, tx *sql.Tx, member entityMembership, outletID int64, today string) ([]outstandingPayableTotals, error) {
	// The schema permits ARS and USD only. Aggregate every outstanding row,
	// with at most two output buckets and no underlying details in the result.
	// SQLite rejects SUM overflow rather than returning a partial total.
	rows, err := tx.QueryContext(ctx, `SELECT p.currency,COUNT(*),CAST(SUM(p.balance_minor) AS BIGINT),
		SUM(CASE WHEN p.due_date_snapshot<>'' AND p.due_date_snapshot<$1 THEN 1 ELSE 0 END),
		CAST(SUM(CASE WHEN p.due_date_snapshot<>'' AND p.due_date_snapshot<$2 THEN p.balance_minor ELSE 0 END) AS BIGINT)
		FROM ap_payables p JOIN outlets o ON o.id=p.outlet_id AND o.entity_id=p.entity_id AND o.active=TRUE
        JOIN outlet_grants g ON g.outlet_id=o.id AND g.membership_id=$3
		WHERE p.entity_id=$4 AND p.outlet_id=$5 AND p.balance_minor>0
		GROUP BY p.currency ORDER BY p.currency`, today, today, member.id, member.ID, outletID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := []outstandingPayableTotals{}
	for rows.Next() {
		var value outstandingPayableTotals
		if err := rows.Scan(&value.Currency, &value.OutstandingCount, &value.OutstandingMinor,
			&value.OverdueCount, &value.OverdueMinor); err != nil {
			return nil, err
		}
		totals = append(totals, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return totals, nil
}
