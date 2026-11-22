package main

import (
	"database/sql"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

func (a *app) registerMCPAccountingReads(add func(string, string, http.HandlerFunc)) {
	add(mcpWorkspaceBase+"/documents", http.MethodGet, a.mcpRead(mcpAccountingDocuments))
	add(mcpWorkspaceBase+"/suppliers", http.MethodGet, a.mcpRead(mcpAccountingSuppliers))
	add(mcpWorkspaceBase+"/payments-summary", http.MethodGet, a.mcpRead(mcpPaymentStatusCounts))
	add(mcpWorkspaceBase+"/reconciliation-summary", http.MethodGet, a.mcpRead(mcpReconciliationStatusCounts))
}

func mcpAccountingCursor(r *http.Request) (int64, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, badInput
	}
	if len(query) == 0 {
		return 0, nil
	}
	if len(query) != 1 || len(query["after_id"]) != 1 {
		return 0, badInput
	}
	cursor, err := strconv.ParseInt(query.Get("after_id"), 10, 64)
	if err != nil || cursor <= 0 {
		return 0, badInput
	}
	return cursor, nil
}

type mcpDocumentMetadata struct {
	ID       int64   `json:"id"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
	Supplier int64   `json:"supplier_id"`
	Issue    string  `json:"issue_date"`
	Due      *string `json:"due_date,omitempty"`
	Currency string  `json:"currency"`
	Total    int64   `json:"total_minor"`
}

func mcpAccountingDocuments(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	cursor, err := mcpAccountingCursor(r)
	if err != nil {
		return nil, err
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT d.id,d.document_type,d.status,d.supplier_id,
		d.issue_date,d.due_date,d.currency,d.total_minor FROM documents d
		JOIN suppliers p ON p.id=d.supplier_id AND p.entity_id=d.entity_id
		WHERE d.entity_id=$1 AND d.outlet_id=$2 AND d.id>$3 AND d.deleted_at IS NULL
		ORDER BY d.id LIMIT $4`, s.ID, outlet, cursor, mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := struct {
		Documents []mcpDocumentMetadata `json:"documents"`
		NextID    int64                 `json:"next_id,omitempty"`
	}{Documents: []mcpDocumentMetadata{}}
	for rows.Next() {
		var d mcpDocumentMetadata
		if err := rows.Scan(&d.ID, &d.Type, &d.Status, &d.Supplier, &d.Issue, &d.Due, &d.Currency, &d.Total); err != nil {
			return nil, err
		}
		result.Documents = append(result.Documents, d)
	}
	if len(result.Documents) > mcpPageSize {
		result.Documents = result.Documents[:mcpPageSize]
		result.NextID = result.Documents[mcpPageSize-1].ID
	}
	return result, rows.Err()
}

func mcpAccountingSuppliers(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	cursor, err := mcpAccountingCursor(r)
	if err != nil {
		return nil, err
	}
	if err := requireSupplierOutlet(tx, s.entityMembership); err != nil {
		return nil, err
	}
	if _, err := mcpWorkspaceOutlet(r, tx, s); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,name FROM suppliers
		WHERE entity_id=$1 AND active=TRUE AND id>$2 ORDER BY id LIMIT $3`, s.ID, cursor, mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := struct {
		Suppliers []mcpPlace `json:"suppliers"`
		NextID    int64      `json:"next_id,omitempty"`
	}{Suppliers: []mcpPlace{}}
	for rows.Next() {
		var item mcpPlace
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			return nil, err
		}
		result.Suppliers = append(result.Suppliers, item)
	}
	if len(result.Suppliers) > mcpPageSize {
		result.Suppliers = result.Suppliers[:mcpPageSize]
		result.NextID = result.Suppliers[mcpPageSize-1].ID
	}
	return result, rows.Err()
}

func mcpPaymentStatusCounts(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	if !slices.Contains(capabilities[s.role], recordPayments) {
		return nil, forbiddenRole
	}
	if err := requireFinancialOutlet(tx, s.entityMembership); err != nil {
		return nil, err
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	var counts struct {
		Draft     int64 `json:"draft"`
		Posted    int64 `json:"posted"`
		Reversed  int64 `json:"reversed"`
		Reversals int64 `json:"reversals"`
	}
	err = tx.QueryRowContext(r.Context(), `SELECT
		COUNT(*) FILTER(WHERE kind='payment' AND status='draft'),
		COUNT(*) FILTER(WHERE kind='payment' AND status='posted'),
		COUNT(*) FILTER(WHERE kind='payment' AND status='reversed'),
		COUNT(*) FILTER(WHERE kind='reversal')
		FROM ap_payments WHERE entity_id=$1 AND outlet_id=$2`, s.ID, outlet).
		Scan(&counts.Draft, &counts.Posted, &counts.Reversed, &counts.Reversals)
	return counts, err
}

func mcpReconciliationStatusCounts(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	if !slices.Contains(capabilities[s.role], "reconcile_financial_data") {
		return nil, forbiddenRole
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	var counts struct {
		Draft  int64 `json:"draft"`
		Review int64 `json:"review"`
		Closed int64 `json:"closed"`
	}
	err = tx.QueryRowContext(r.Context(), `SELECT
		COUNT(*) FILTER(WHERE r.state='draft'),
		COUNT(*) FILTER(WHERE r.state='review'),
		COUNT(*) FILTER(WHERE r.state='closed')`+mcpReconciliationRunScope,
		s.ID, outlet, s.id, s.outlet.Valid).Scan(&counts.Draft, &counts.Review, &counts.Closed)
	return counts, err
}
