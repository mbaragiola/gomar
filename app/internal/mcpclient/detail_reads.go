package mcpclient

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

type DocumentDetails struct {
	ID         int64   `json:"id"`
	Revision   int64   `json:"revision"`
	Status     string  `json:"status"`
	Type       string  `json:"type"`
	Reference  string  `json:"reference"`
	IssueDate  string  `json:"issue_date"`
	DueDate    *string `json:"due_date,omitempty"`
	Currency   string  `json:"currency"`
	NetMinor   *int64  `json:"net_minor,omitempty"`
	TaxMinor   *int64  `json:"tax_minor,omitempty"`
	TotalMinor int64   `json:"total_minor"`
}

type ReconciliationCounts struct {
	Confirmed  int64  `json:"confirmed"`
	Rejected   int64  `json:"rejected"`
	Unresolved *int64 `json:"unresolved,omitempty"`
	Outlets    int64  `json:"outlets"`
	Sources    int64  `json:"sources"`
	Rows       int64  `json:"rows"`
}

type ReconciliationRun struct {
	ID        int64                `json:"id"`
	Revision  int64                `json:"revision"`
	Status    string               `json:"status"`
	CreatedAt string               `json:"created_at"`
	UpdatedAt *string              `json:"updated_at,omitempty"`
	ClosedAt  *string              `json:"closed_at,omitempty"`
	Counts    ReconciliationCounts `json:"counts"`
}

type ReconciliationRuns struct {
	Runs   []ReconciliationRun `json:"runs"`
	NextID int64               `json:"next_id,omitempty"`
}

func (c *Client) ReadDocument(ctx context.Context, entity, outlet, document int64) (DocumentDetails, error) {
	var result DocumentDetails
	if document <= 0 {
		return result, errors.New("indicá un document_id devuelto por gomar_list_documents")
	}
	var payload struct {
		DocumentDetails
		Revision   *int64 `json:"revision"`
		TotalMinor *int64 `json:"total_minor"`
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/documents/"+strconv.FormatInt(document, 10), nil, &payload); err != nil {
		return DocumentDetails{}, err
	}
	if payload.Revision == nil || payload.TotalMinor == nil {
		return DocumentDetails{}, invalidResponse
	}
	result = payload.DocumentDetails
	result.Revision = *payload.Revision
	result.TotalMinor = *payload.TotalMinor
	if result.ID != document || result.Revision < 0 || (result.Status != "draft" && result.Status != "reviewed") ||
		(result.Type != "invoice" && result.Type != "debit_note" && result.Type != "credit_note") ||
		strings.TrimSpace(result.Reference) == "" || utf8.RuneCountInString(result.Reference) > 100 ||
		!accountingDate(result.IssueDate) || (result.DueDate != nil && !accountingDate(*result.DueDate)) ||
		(result.Currency != "ARS" && result.Currency != "USD") || result.TotalMinor < 0 ||
		(result.NetMinor != nil && *result.NetMinor < 0) || (result.TaxMinor != nil && *result.TaxMinor < 0) {
		return DocumentDetails{}, invalidResponse
	}
	return result, nil
}

func validReconciliationRun(r ReconciliationRun) bool {
	if r.ID <= 0 || r.Revision <= 0 || (r.Status != "draft" && r.Status != "review" && r.Status != "closed") ||
		!validTime(r.CreatedAt) || (r.UpdatedAt != nil && !validTime(*r.UpdatedAt)) || (r.ClosedAt != nil && !validTime(*r.ClosedAt)) {
		return false
	}
	c := r.Counts
	return c.Confirmed >= 0 && c.Rejected >= 0 && (c.Unresolved == nil || *c.Unresolved >= 0) &&
		c.Outlets >= 1 && c.Sources >= 0 && c.Rows >= 0
}

func (c *Client) ListReconciliationRuns(ctx context.Context, entity, outlet int64, after *int64) (ReconciliationRuns, error) {
	var result ReconciliationRuns
	q, last, err := accountingQuery(after)
	if err != nil {
		return result, err
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/reconciliation-runs", q, &result); err != nil {
		return ReconciliationRuns{}, err
	}
	for _, item := range result.Runs {
		if item.ID <= last || !validReconciliationRun(item) {
			return ReconciliationRuns{}, invalidResponse
		}
		last = item.ID
	}
	if result.Runs == nil || !validAccountingPage(result.NextID, last, len(result.Runs)) {
		return ReconciliationRuns{}, invalidResponse
	}
	return result, nil
}

func (c *Client) ReadReconciliationRun(ctx context.Context, entity, outlet, run int64) (ReconciliationRun, error) {
	var result ReconciliationRun
	if run <= 0 {
		return result, errors.New("indicá un run_id devuelto por gomar_list_reconciliation_runs")
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/reconciliation-runs/"+strconv.FormatInt(run, 10), nil, &result); err != nil {
		return ReconciliationRun{}, err
	}
	if result.ID != run || !validReconciliationRun(result) {
		return ReconciliationRun{}, invalidResponse
	}
	return result, nil
}
