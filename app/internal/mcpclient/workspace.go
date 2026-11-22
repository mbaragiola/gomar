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

const workspacePageSize = 30

type ArticleRef struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Version   int64  `json:"version"`
	UpdatedAt string `json:"updated_at"`
}

type KnowledgeList struct {
	Articles  []ArticleRef `json:"articles"`
	Truncated bool         `json:"truncated"`
}

type Article struct {
	ArticleRef
	Body string `json:"body"`
}

type StockProduct struct {
	Named
	Enabled bool   `json:"enabled"`
	Minimum int64  `json:"minimum"`
	Balance int64  `json:"balance"`
	Alert   string `json:"alert,omitempty"`
}

type Stock struct {
	Enabled       bool           `json:"enabled"`
	Products      []StockProduct `json:"products"`
	NextProductID int64          `json:"next_product_id,omitempty"`
}

type DeliveryOrder struct {
	ID         int64  `json:"id"`
	Provider   string `json:"provider"`
	Status     string `json:"status"`
	ReceivedAt string `json:"received_at"`
	UpdatedAt  string `json:"updated_at"`
}

type Delivery struct {
	Orders    []DeliveryOrder `json:"orders"`
	Truncated bool            `json:"truncated"`
}

type PayableTotal struct {
	Currency         string `json:"currency"`
	OutstandingCount int64  `json:"outstanding_count"`
	OutstandingMinor int64  `json:"outstanding_minor"`
	OverdueCount     int64  `json:"overdue_count"`
	OverdueMinor     int64  `json:"overdue_minor"`
}

type Payables struct {
	AsOf   string         `json:"as_of"`
	Totals []PayableTotal `json:"totals"`
}

func (c *Client) Knowledge(ctx context.Context, entity, outlet int64, search string) (KnowledgeList, error) {
	var result KnowledgeList
	search = strings.TrimSpace(search)
	query := url.Values{}
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 120 || strings.ContainsRune(search, 0) {
		return result, errors.New("usá hasta 120 caracteres para buscar en la base de conocimiento")
	}
	if search != "" {
		query.Set("q", search)
	}
	err := c.getWorkspace(ctx, entity, outlet, "/knowledge", query, &result)
	if err == nil {
		if result.Articles == nil || len(result.Articles) > workspacePageSize {
			return result, invalidResponse
		}
		for _, a := range result.Articles {
			if !validArticle(a) {
				return result, invalidResponse
			}
		}
	}
	return result, err
}

func validTime(s string) bool { _, err := time.Parse(time.RFC3339Nano, s); return err == nil }

func validArticle(a ArticleRef) bool {
	return a.ID > 0 && a.Version > 0 && strings.TrimSpace(a.Title) != "" &&
		utf8.RuneCountInString(a.Title) <= 120 && validTime(a.UpdatedAt)
}

func (c *Client) Article(ctx context.Context, entity, outlet, article int64) (Article, error) {
	var result Article
	if article <= 0 {
		return result, errors.New("indicá un article_id devuelto por gomar_search_knowledge")
	}
	err := c.getWorkspace(ctx, entity, outlet, "/knowledge/"+strconv.FormatInt(article, 10), nil, &result)
	if err == nil && (!validArticle(result.ArticleRef) || result.ID != article ||
		strings.TrimSpace(result.Body) == "" || utf8.RuneCountInString(result.Body) > 12000) {
		err = invalidResponse
	}
	return result, err
}

func (c *Client) Stock(ctx context.Context, entity, outlet int64, after *int64) (Stock, error) {
	var result Stock
	query := url.Values{}
	var last int64
	if after != nil {
		if *after <= 0 {
			return result, errors.New("usá el next_product_id de la página anterior u omití el cursor")
		}
		last = *after
		query.Set("after_product_id", strconv.FormatInt(*after, 10))
	}
	err := c.getWorkspace(ctx, entity, outlet, "/stock", query, &result)
	if err != nil {
		return result, err
	}
	if result.Products == nil || len(result.Products) > workspacePageSize {
		return result, invalidResponse
	}
	for _, p := range result.Products {
		if p.ID <= last || strings.TrimSpace(p.Name) == "" || p.Minimum < 0 || (p.Alert != "" && p.Alert != "low" && p.Alert != "negative") {
			return result, invalidResponse
		}
		last = p.ID
	}
	if result.NextProductID != 0 && (len(result.Products) != workspacePageSize || result.NextProductID != last) {
		return result, invalidResponse
	}
	return result, nil
}

type DeliveryStatusSummary struct {
	WindowLimit int `json:"window_limit"`
	Received    int `json:"received"`
	Accepted    int `json:"accepted"`
	Cancelled   int `json:"cancelled"`
}

func (c *Client) DeliveryStatus(ctx context.Context, entity, outlet int64) (DeliveryStatusSummary, error) {
	var w struct {
		WindowLimit *int `json:"window_limit"`
		Received    *int `json:"received"`
		Accepted    *int `json:"accepted"`
		Cancelled   *int `json:"cancelled"`
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/delivery-status-summary", nil, &w); err != nil {
		return DeliveryStatusSummary{}, err
	}
	if w.WindowLimit == nil || *w.WindowLimit != 100 || w.Received == nil || w.Accepted == nil || w.Cancelled == nil {
		return DeliveryStatusSummary{}, invalidResponse
	}
	remaining := *w.WindowLimit
	for _, count := range []int{*w.Received, *w.Accepted, *w.Cancelled} {
		if count < 0 || count > remaining {
			return DeliveryStatusSummary{}, invalidResponse
		}
		remaining -= count
	}
	return DeliveryStatusSummary{WindowLimit: *w.WindowLimit, Received: *w.Received, Accepted: *w.Accepted, Cancelled: *w.Cancelled}, nil
}

func (c *Client) Delivery(ctx context.Context, entity, outlet int64) (Delivery, error) {
	var result Delivery
	err := c.getWorkspace(ctx, entity, outlet, "/delivery-orders", nil, &result)
	if err != nil {
		return result, err
	}
	if result.Orders == nil || len(result.Orders) > workspacePageSize {
		return result, invalidResponse
	}
	for _, o := range result.Orders {
		if o.ID <= 0 || (o.Status != "accepted" && o.Status != "received" && o.Status != "cancelled") ||
			o.Provider == "" || !validTime(o.ReceivedAt) || !validTime(o.UpdatedAt) {
			return result, invalidResponse
		}
	}
	return result, nil
}

func (c *Client) Payables(ctx context.Context, entity, outlet int64) (Payables, error) {
	var result Payables
	err := c.getWorkspace(ctx, entity, outlet, "/payables-summary", nil, &result)
	if err != nil {
		return result, err
	}
	if _, err := time.Parse(time.DateOnly, result.AsOf); err != nil || result.Totals == nil || len(result.Totals) > 2 {
		return result, invalidResponse
	}
	seen := map[string]bool{}
	for _, p := range result.Totals {
		if (p.Currency != "ARS" && p.Currency != "USD") || seen[p.Currency] || p.OutstandingCount <= 0 || p.OutstandingMinor <= 0 ||
			p.OverdueCount < 0 || p.OverdueCount > p.OutstandingCount || p.OverdueMinor < 0 || p.OverdueMinor > p.OutstandingMinor {
			return result, invalidResponse
		}
		seen[p.Currency] = true
	}
	return result, nil
}

func (c *Client) getWorkspace(ctx context.Context, entity, outlet int64, endpoint string, query url.Values, result any) error {
	if entity <= 0 || outlet <= 0 {
		return errors.New("indicá entity_id y outlet_id devueltos por las herramientas de Gomar")
	}
	return c.get(ctx, entityPath(entity)+"/outlets/"+strconv.FormatInt(outlet, 10)+endpoint, query, result)
}
