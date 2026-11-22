package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

func (a *app) registerMCPDetailReads(add func(string, string, http.HandlerFunc)) {
	add(mcpWorkspaceBase+"/documents/{document}", http.MethodGet, a.mcpRead(mcpReadDocument))
	add(mcpWorkspaceBase+"/reconciliation-runs", http.MethodGet, a.mcpRead(mcpListReconciliationRuns))
	add(mcpWorkspaceBase+"/reconciliation-runs/{run}", http.MethodGet, a.mcpRead(mcpReadReconciliationRun))
}

var mcpMissingDocument = problem{404, "No se encontró el comprobante."}
var mcpMissingReconciliation = problem{404, "No se encontró la conciliación."}

func mcpReadDocument(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	doc, err := pathID(r, "document")
	if err != nil || doc <= 0 {
		return nil, mcpMissingDocument
	}
	var item mcpclient.DocumentDetails
	var letter string
	var point, number int64
	err = tx.QueryRowContext(r.Context(), `SELECT d.id,d.revision,d.status,d.document_type,d.letter,d.point_of_sale,d.number,
 d.issue_date,d.due_date,d.currency,d.subtotal_minor,d.tax_minor,d.total_minor
 FROM documents d JOIN suppliers p ON p.id=d.supplier_id AND p.entity_id=d.entity_id
 WHERE d.entity_id=$1 AND d.outlet_id=$2 AND d.id=$3 AND d.deleted_at IS NULL`, s.ID, outlet, doc).
		Scan(&item.ID, &item.Revision, &item.Status, &item.Type, &letter, &point, &number,
			&item.IssueDate, &item.DueDate, &item.Currency, &item.NetMinor, &item.TaxMinor, &item.TotalMinor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, mcpMissingDocument
	}
	if err != nil {
		return nil, err
	}
	label, ok := documentTypes[item.Type]
	if !ok || label == "" {
		return nil, errors.New("unexpected document type")
	}
	if letter != "" {
		if len(letter) != 1 || letter[0] < 'A' || letter[0] > 'Z' {
			return nil, errors.New("invalid document reference")
		}
		label += " " + letter
	}
	for _, date := range []*string{&item.IssueDate, item.DueDate} {
		if date != nil {
			if _, err := time.Parse(time.DateOnly, *date); err != nil {
				return nil, errors.New("invalid document date")
			}
		}
	}
	item.Reference = fmt.Sprintf("%s %05d-%08d", label, point, number)
	return item, nil
}

// This same predicate gates counts and record reads. An entity-wide token
// may read a shared run only if every participant outlet remains granted.
const mcpReconciliationRunScope = `
 FROM reconciliation_read_runs r WHERE r.entity_id=$1 AND r.state<>'deleted'
 AND EXISTS(SELECT 1 FROM reconciliation_read_outlets ro WHERE ro.run_id=r.id AND ro.outlet_id=$2)
 AND NOT EXISTS (
  SELECT 1 FROM reconciliation_read_outlets ro WHERE ro.run_id=r.id AND (
   ($4 AND ro.outlet_id<>$2) OR NOT EXISTS(
    SELECT 1 FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id
    WHERE o.id=ro.outlet_id AND o.entity_id=r.entity_id AND o.active=TRUE AND g.membership_id=$3
   )
  )
 )`

const mcpReconciliationMetadata = `SELECT r.id,r.revision,r.state,r.created_at,r.updated_at,r.closed_at,
 r.confirmed_count,r.rejected_count,r.unresolved_count,
 (SELECT COUNT(*) FROM reconciliation_read_outlets ro WHERE ro.run_id=r.id),
 (SELECT COUNT(*) FROM reconciliation_read_sources rs WHERE rs.run_id=r.id),
 (SELECT CAST(COALESCE(SUM(rs.row_count),0) AS BIGINT) FROM reconciliation_read_sources rs WHERE rs.run_id=r.id)`

func mcpReconciliationOutlet(r *http.Request, tx *sql.Tx, s mcpTokenScope) (int64, error) {
	if !slices.Contains(capabilities[s.role], "reconcile_financial_data") {
		return 0, forbiddenRole
	}
	return mcpWorkspaceOutlet(r, tx, s)
}

func mcpScanReconciliationMetadata(row interface{ Scan(...any) error }) (mcpclient.ReconciliationRun, error) {
	var v mcpclient.ReconciliationRun
	err := row.Scan(&v.ID, &v.Revision, &v.Status, &v.CreatedAt, &v.UpdatedAt, &v.ClosedAt,
		&v.Counts.Confirmed, &v.Counts.Rejected, &v.Counts.Unresolved, &v.Counts.Outlets, &v.Counts.Sources, &v.Counts.Rows)
	if err != nil {
		return v, err
	}
	for _, stamp := range []*string{&v.CreatedAt, v.UpdatedAt, v.ClosedAt} {
		if _, err := reconciliationWireTime(stamp); err != nil {
			return v, err
		}
	}
	return v, nil
}

func mcpListReconciliationRuns(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpReconciliationOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	after, err := mcpAccountingCursor(r)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(r.Context(), mcpReconciliationMetadata+mcpReconciliationRunScope+` AND r.id>$5 ORDER BY r.id LIMIT $6`,
		s.ID, outlet, s.id, s.outlet.Valid, after, mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := mcpclient.ReconciliationRuns{Runs: []mcpclient.ReconciliationRun{}}
	for rows.Next() {
		v, err := mcpScanReconciliationMetadata(rows)
		if err != nil {
			return nil, err
		}
		result.Runs = append(result.Runs, v)
	}
	if len(result.Runs) > mcpPageSize {
		result.Runs = result.Runs[:mcpPageSize]
		result.NextID = result.Runs[mcpPageSize-1].ID
	}
	return result, rows.Err()
}

func mcpReadReconciliationRun(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	outlet, err := mcpReconciliationOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	run, err := pathID(r, "run")
	if err != nil || run <= 0 {
		return nil, mcpMissingReconciliation
	}
	v, err := mcpScanReconciliationMetadata(tx.QueryRowContext(r.Context(), mcpReconciliationMetadata+mcpReconciliationRunScope+` AND r.id=$5`,
		s.ID, outlet, s.id, s.outlet.Valid, run))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, mcpMissingReconciliation
	}
	return v, err
}
