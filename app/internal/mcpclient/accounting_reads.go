package mcpclient

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type DocumentMetadata struct {
	ID       int64   `json:"id"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
	Supplier int64   `json:"supplier_id"`
	Issue    string  `json:"issue_date"`
	Due      *string `json:"due_date,omitempty"`
	Currency string  `json:"currency"`
	Total    int64   `json:"total_minor"`
}

type DocumentList struct {
	Documents []DocumentMetadata `json:"documents"`
	NextID    int64              `json:"next_id,omitempty"`
}

type DocumentCurrencyTotal struct {
	Currency   string `json:"currency"`
	Count      int64  `json:"count"`
	TotalMinor string `json:"total_minor"`
}

type DocumentTotals struct {
	Count  int64                   `json:"count"`
	Totals []DocumentCurrencyTotal `json:"totals"`
}

type SupplierList struct {
	Suppliers []SupplierReference `json:"suppliers"`
	NextID    int64               `json:"next_id,omitempty"`
}

type SupplierReference struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Revision *int64 `json:"revision,omitempty"`
}

type PaymentStatusCounts struct {
	Draft     int64 `json:"draft"`
	Posted    int64 `json:"posted"`
	Reversed  int64 `json:"reversed"`
	Reversals int64 `json:"reversals"`
}

type ReconciliationStatusCounts struct {
	Draft  int64 `json:"draft"`
	Review int64 `json:"review"`
	Closed int64 `json:"closed"`
}

func accountingQuery(after *int64) (url.Values, int64, error) {
	if after == nil {
		return nil, 0, nil
	}
	if *after <= 0 {
		return nil, 0, errors.New("usá el next_id de la página anterior u omití el cursor")
	}
	return url.Values{"after_id": {strconv.FormatInt(*after, 10)}}, *after, nil
}

func validAccountingPage(next, last int64, size int) bool {
	return size <= workspacePageSize && (next == 0 || size == workspacePageSize && next == last)
}

func accountingDate(value string) bool {
	_, err := time.Parse(time.DateOnly, value)
	return err == nil
}

func (c *Client) Documents(ctx context.Context, entity, outlet int64, after *int64) (DocumentList, error) {
	var result DocumentList
	query, last, err := accountingQuery(after)
	if err != nil {
		return result, err
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/documents", query, &result); err != nil {
		return result, err
	}
	for _, d := range result.Documents {
		if d.ID <= last || d.Supplier <= 0 || (d.Type != "invoice" && d.Type != "credit_note" && d.Type != "debit_note") ||
			(d.Status != "draft" && d.Status != "reviewed") || !accountingDate(d.Issue) || (d.Due != nil && !accountingDate(*d.Due)) ||
			(d.Currency != "ARS" && d.Currency != "USD") || d.Total < 0 {
			return DocumentList{}, invalidResponse
		}
		last = d.ID
	}
	if result.Documents == nil || !validAccountingPage(result.NextID, last, len(result.Documents)) {
		return DocumentList{}, invalidResponse
	}
	return result, nil
}

func validDocumentMinor(value string) bool {
	if value == "0" {
		return true
	}
	digits := strings.TrimPrefix(value, "-")
	// A signed int64 count of int64 input amounts fits within 38 digits.
	if len(digits) == 0 || len(digits) > 38 || digits[0] == '0' {
		return false
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (c *Client) DocumentTotals(ctx context.Context, entity, outlet int64) (DocumentTotals, error) {
	var wire struct {
		Count  *int64                  `json:"count"`
		Totals []DocumentCurrencyTotal `json:"totals"`
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/document-totals", nil, &wire); err != nil {
		return DocumentTotals{}, err
	}
	if wire.Count == nil || *wire.Count < 0 || wire.Totals == nil || len(wire.Totals) > 2 {
		return DocumentTotals{}, invalidResponse
	}
	remaining, previous := *wire.Count, ""
	for _, bucket := range wire.Totals {
		if (bucket.Currency != "ARS" && bucket.Currency != "USD") || bucket.Currency <= previous ||
			bucket.Count <= 0 || bucket.Count > remaining || !validDocumentMinor(bucket.TotalMinor) {
			return DocumentTotals{}, invalidResponse
		}
		remaining -= bucket.Count
		previous = bucket.Currency
	}
	if remaining != 0 {
		return DocumentTotals{}, invalidResponse
	}
	return DocumentTotals{Count: *wire.Count, Totals: wire.Totals}, nil
}

func (c *Client) Suppliers(ctx context.Context, entity, outlet int64, after *int64) (SupplierList, error) {
	var result SupplierList
	query, last, err := accountingQuery(after)
	if err != nil {
		return result, err
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/suppliers", query, &result); err != nil {
		return result, err
	}
	for _, s := range result.Suppliers {
		if s.ID <= last || strings.TrimSpace(s.Name) == "" || !utf8.ValidString(s.Name) || s.Revision != nil && *s.Revision < 1 {
			return SupplierList{}, invalidResponse
		}
		last = s.ID
	}
	if result.Suppliers == nil || !validAccountingPage(result.NextID, last, len(result.Suppliers)) {
		return SupplierList{}, invalidResponse
	}
	return result, nil
}

func (c *Client) PaymentStatus(ctx context.Context, entity, outlet int64) (PaymentStatusCounts, error) {
	var result *PaymentStatusCounts
	err := c.getWorkspace(ctx, entity, outlet, "/payments-summary", nil, &result)
	if err != nil {
		return PaymentStatusCounts{}, err
	}
	if result == nil || result.Draft < 0 || result.Posted < 0 || result.Reversed < 0 || result.Reversals < 0 {
		return PaymentStatusCounts{}, invalidResponse
	}
	return *result, nil
}

func (c *Client) ReconciliationStatus(ctx context.Context, entity, outlet int64) (ReconciliationStatusCounts, error) {
	var result *ReconciliationStatusCounts
	err := c.getWorkspace(ctx, entity, outlet, "/reconciliation-summary", nil, &result)
	if err != nil {
		return ReconciliationStatusCounts{}, err
	}
	if result == nil || result.Draft < 0 || result.Review < 0 || result.Closed < 0 {
		return ReconciliationStatusCounts{}, invalidResponse
	}
	return *result, nil
}
