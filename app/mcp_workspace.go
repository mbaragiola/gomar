package main

import (
	"database/sql"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const mcpWorkspaceBase = "/api/v1/mcp/entities/{entity}/outlets/{outlet}"
const mcpPageSize = 30

func (a *app) registerMCPWorkspace(add func(string, string, http.HandlerFunc)) {
	add(mcpWorkspaceBase+"/knowledge", http.MethodGet, a.mcpRead(mcpKnowledgeList))
	add(mcpWorkspaceBase+"/knowledge/{article}", http.MethodGet, a.mcpRead(mcpKnowledgeDetail))
	if a.enablePOS && a.enableStock {
		add(mcpWorkspaceBase+"/stock", http.MethodGet, a.mcpRead(mcpStock))
		add(mcpWorkspaceBase+"/stock-alert-summary", http.MethodGet, a.mcpRead(mcpStockAlertSummary))
	}
	if a.enableDelivery {
		add(mcpWorkspaceBase+"/delivery-orders", http.MethodGet, a.mcpRead(mcpDelivery))
		add(mcpWorkspaceBase+"/delivery-status-summary", http.MethodGet, a.mcpRead(mcpDeliveryStatusSummary))
	}
	add(mcpWorkspaceBase+"/payables-summary", http.MethodGet, a.mcpRead(a.mcpPayablesSummary))
}

func mcpWorkspaceOutlet(r *http.Request, tx *sql.Tx, s mcpTokenScope) (int64, error) {
	id, err := pathID(r, "outlet")
	if err != nil || s.outlet.Valid && id != s.outlet.Int64 {
		return 0, missingOutlet
	}
	return id, requireSelectedDocumentOutlet(tx, s.entityMembership, id)
}

func mcpOptionalQuery(r *http.Request, key string) (string, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", badInput
	}
	if len(q) == 0 {
		return "", nil
	}
	if len(q) != 1 || len(q[key]) != 1 {
		return "", badInput
	}
	return q.Get(key), nil
}

type mcpArticle struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Version   int64  `json:"version"`
	UpdatedAt string `json:"updated_at"`
}

func mcpKnowledgeList(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	q, err := mcpOptionalQuery(r, "q")
	q = strings.TrimSpace(q)
	if err != nil || !utf8.ValidString(q) || utf8.RuneCountInString(q) > 120 || strings.ContainsRune(q, 0) {
		return nil, badInput
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,title,version,updated_at FROM knowledge_articles
  WHERE outlet_id=$1 AND ($2='' OR replace(search_text,$2,'')<>search_text)
  ORDER BY id DESC LIMIT $3`, outlet, strings.ToLower(q), mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := struct {
		Articles  []mcpArticle `json:"articles"`
		Truncated bool         `json:"truncated"`
	}{Articles: []mcpArticle{}}
	for rows.Next() {
		var item mcpArticle
		if err := rows.Scan(&item.ID, &item.Title, &item.Version, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result.Articles = append(result.Articles, item)
	}
	if len(result.Articles) > mcpPageSize {
		result.Articles = result.Articles[:mcpPageSize]
		result.Truncated = true
	}
	return result, rows.Err()
}

func mcpKnowledgeDetail(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	a, err := pathID(r, "article")
	if err != nil {
		return nil, missingKnowledge
	}
	if _, err = mcpWorkspaceOutlet(r, tx, s); err != nil {
		return nil, err
	}
	_, value, err := viewKnowledge(r, tx, s.entityMembership, nil)
	if err != nil {
		return nil, err
	}
	article := value.(knowledgeArticle)
	if article.ID != a {
		return nil, missingKnowledge
	}
	return struct {
		mcpArticle
		Body string `json:"body"`
	}{mcpArticle{article.ID, article.Title, article.Version, article.UpdatedAt}, article.Body}, nil
}

type mcpStockItem struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Minimum int64  `json:"minimum"`
	Balance int64  `json:"balance"`
	Alert   string `json:"alert,omitempty"`
}

func mcpStockAlertSummary(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if !slices.Contains([]string{"administration", "purchasing", "accounting"}, s.role) {
		return nil, forbiddenRole
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, badInput
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	enabled, err := stockOutletActive(r.Context(), tx, outlet)
	if err != nil {
		return nil, err
	}
	result := struct {
		Enabled          bool  `json:"enabled"`
		Tracked          int64 `json:"tracked"`
		Negative         int64 `json:"negative"`
		AtOrBelowMinimum int64 `json:"at_or_below_minimum"`
		Healthy          int64 `json:"healthy"`
	}{Enabled: enabled}
	if !enabled {
		return result, nil
	}
	err = tx.QueryRowContext(r.Context(), `SELECT count(*),count(*) FILTER(WHERE balance<0),
  count(*) FILTER(WHERE balance>=0 AND balance<=minimum),count(*) FILTER(WHERE balance>minimum)
  FROM (SELECT t.minimum,COALESCE((SELECT CAST(SUM(delta) AS BIGINT) FROM stock_movements x
    WHERE x.outlet_id=t.outlet_id AND x.product_id=t.product_id),0) AS balance
  FROM stock_products t JOIN pos_products p ON p.id=t.product_id AND p.outlet_id=t.outlet_id
  JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
  WHERE t.outlet_id=$1 AND t.enabled) selected`, outlet).
		Scan(&result.Tracked, &result.Negative, &result.AtOrBelowMinimum, &result.Healthy)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func mcpStock(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if !slices.Contains([]string{"administration", "purchasing", "accounting"}, s.role) {
		return nil, forbiddenRole
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	cursor, err := mcpOptionalQuery(r, "after_product_id")
	if err != nil {
		return nil, err
	}
	var after int64
	if cursor != "" {
		after, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || after <= 0 {
			return nil, badInput
		}
	}
	enabled, err := stockOutletActive(r.Context(), tx, outlet)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT t.product_id,v.name,t.enabled,t.minimum,
  COALESCE((SELECT CAST(SUM(delta) AS BIGINT) FROM stock_movements x WHERE x.outlet_id=t.outlet_id AND x.product_id=t.product_id),0)
  FROM stock_products t JOIN pos_products p ON p.id=t.product_id AND p.outlet_id=t.outlet_id
  JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id AND v.revision=p.current_revision
  WHERE t.outlet_id=$1 AND t.product_id>$2 ORDER BY t.product_id LIMIT $3`, outlet, after, mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := struct {
		Enabled       bool           `json:"enabled"`
		Products      []mcpStockItem `json:"products"`
		NextProductID int64          `json:"next_product_id,omitempty"`
	}{Enabled: enabled, Products: []mcpStockItem{}}
	for rows.Next() {
		var item mcpStockItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Enabled, &item.Minimum, &item.Balance); err != nil {
			return nil, err
		}
		if enabled && item.Enabled {
			switch {
			case item.Balance < 0:
				item.Alert = "negative"
			case item.Balance <= item.Minimum:
				item.Alert = "low"
			}
		}
		result.Products = append(result.Products, item)
	}
	if len(result.Products) > mcpPageSize {
		result.Products = result.Products[:mcpPageSize]
		result.NextProductID = result.Products[len(result.Products)-1].ID
	}
	return result, rows.Err()
}

type mcpDeliveryOrder struct {
	ID         int64  `json:"id"`
	Provider   string `json:"provider"`
	Status     string `json:"status"`
	ReceivedAt string `json:"received_at"`
	UpdatedAt  string `json:"updated_at"`
}

func mcpDeliveryStatusSummary(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, badInput
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	var result struct {
		WindowLimit int `json:"window_limit"`
		Received    int `json:"received"`
		Accepted    int `json:"accepted"`
		Cancelled   int `json:"cancelled"`
	}
	result.WindowLimit = deliveryInboxLimit
	// Use the same snapshot window as the native inbox, without reading any
	// order identifiers, provider details or item data into the response.
	err = tx.QueryRowContext(r.Context(), `SELECT count(*) FILTER (WHERE NOT cancelled AND provider='rappi'),
 count(*) FILTER (WHERE NOT cancelled AND provider<>'rappi'),count(*) FILTER (WHERE cancelled)
 FROM (SELECT provider,cancelled FROM delivery_orders WHERE outlet_id=$1`+deliveryInboxOrder+` LIMIT $2) inbox`,
		outlet, deliveryInboxLimit).Scan(&result.Received, &result.Accepted, &result.Cancelled)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func mcpDelivery(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,provider,cancelled,received_at,updated_at FROM delivery_orders
  WHERE outlet_id=$1 ORDER BY updated_at DESC,id DESC LIMIT $2`, outlet, mcpPageSize+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := struct {
		Orders    []mcpDeliveryOrder `json:"orders"`
		Truncated bool               `json:"truncated"`
	}{Orders: []mcpDeliveryOrder{}}
	for rows.Next() {
		var item mcpDeliveryOrder
		var cancelled bool
		if err := rows.Scan(&item.ID, &item.Provider, &cancelled, &item.ReceivedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		switch {
		case cancelled:
			item.Status = "cancelled"
		case item.Provider == "rappi":
			item.Status = "received"
		default:
			item.Status = "accepted"
		}
		result.Orders = append(result.Orders, item)
	}
	if len(result.Orders) > mcpPageSize {
		result.Orders = result.Orders[:mcpPageSize]
		result.Truncated = true
	}
	return result, rows.Err()
}

type mcpPayableTotal struct {
	Currency         string `json:"currency"`
	OutstandingCount int64  `json:"outstanding_count"`
	OutstandingMinor int64  `json:"outstanding_minor"`
	OverdueCount     int64  `json:"overdue_count"`
	OverdueMinor     int64  `json:"overdue_minor"`
}

func (a *app) mcpPayablesSummary(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
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
	today := a.payableNow().In(payablesLocation).Format(payablesDate)
	totals, err := queryOutstandingPayableTotals(r.Context(), tx, s.entityMembership, outlet, today)
	if err != nil {
		return nil, err
	}
	result := struct {
		AsOf   string            `json:"as_of"`
		Totals []mcpPayableTotal `json:"totals"`
	}{AsOf: today, Totals: make([]mcpPayableTotal, 0, len(totals))}
	for _, total := range totals {
		result.Totals = append(result.Totals, mcpPayableTotal(total))
	}
	return result, nil
}
