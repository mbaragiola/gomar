package mcpclient

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const buenosAires = "America/Argentina/Buenos_Aires"

type ReservationCount struct {
	Status         string `json:"status"`
	RequestCount   int64  `json:"request_count"`
	PartySizeTotal int64  `json:"party_size_total"`
}

type ReservationStatus struct {
	Date     string             `json:"date"`
	Timezone string             `json:"timezone"`
	Counts   []ReservationCount `json:"counts"`
}

type PublishedMenu struct {
	Published bool   `json:"published"`
	Filename  string `json:"filename,omitempty"`
	SizeBytes int    `json:"size_bytes,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type POSProduct struct {
	Named
	PriceMinor int64 `json:"price_minor"`
	Revision   int64 `json:"revision"`
	Active     bool  `json:"active"`
}

type POSCatalog struct {
	Currency      string       `json:"currency"`
	Products      []POSProduct `json:"products"`
	NextProductID int64        `json:"next_product_id,omitempty"`
}

type MovementKind struct {
	Kind          string `json:"kind"`
	MovementCount int64  `json:"movement_count"`
	NetUnits      int64  `json:"net_units"`
}

type StockMovements struct {
	ProductID        int64          `json:"product_id"`
	Timezone         string         `json:"timezone"`
	StartDate        string         `json:"start_date"`
	EndDateExclusive string         `json:"end_date_exclusive"`
	Kinds            []MovementKind `json:"kinds"`
}

func parseReportDate(raw string) (time.Time, error) {
	d, err := time.Parse(time.DateOnly, raw)
	if err != nil || d.Format(time.DateOnly) != raw || d.Year() < 2000 || d.Year() > 9998 {
		return time.Time{}, errors.New("indicá una fecha como AAAA-MM-DD entre 2000 y 9998")
	}
	return d, nil
}

func (c *Client) ReservationStatus(ctx context.Context, entity, outlet int64, date string) (ReservationStatus, error) {
	var result ReservationStatus
	if _, err := parseReportDate(date); err != nil {
		return result, err
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/reservation-status", url.Values{"date": {date}}, &result); err != nil {
		return result, err
	}
	if result.Date != date || result.Timezone != buenosAires || len(result.Counts) != 3 {
		return result, invalidResponse
	}
	for i, k := range []string{"pending", "confirmed", "cancelled"} {
		item := result.Counts[i]
		if item.Status != k || item.RequestCount < 0 || item.PartySizeTotal < item.RequestCount ||
			(item.RequestCount == 0 && item.PartySizeTotal != 0) {
			return result, invalidResponse
		}
	}
	return result, nil
}

func (c *Client) PublishedMenu(ctx context.Context, entity, outlet int64) (PublishedMenu, error) {
	var raw struct {
		Published *bool  `json:"published"`
		Filename  string `json:"filename"`
		SizeBytes int    `json:"size_bytes"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/published-menu", nil, &raw); err != nil {
		return PublishedMenu{}, err
	}
	if raw.Published == nil {
		return PublishedMenu{}, invalidResponse
	}
	result := PublishedMenu{Published: *raw.Published, Filename: raw.Filename, SizeBytes: raw.SizeBytes, UpdatedAt: raw.UpdatedAt}
	if result.Published {
		if result.Filename != "menu.pdf" || result.SizeBytes < 16 || result.SizeBytes > 4<<20 || !validTime(result.UpdatedAt) {
			return PublishedMenu{}, invalidResponse
		}
	} else if result.Filename != "" || result.SizeBytes != 0 || result.UpdatedAt != "" {
		return PublishedMenu{}, invalidResponse
	}
	return result, nil
}

func (c *Client) POSCatalog(ctx context.Context, entity, outlet int64, after *int64) (POSCatalog, error) {
	var result POSCatalog
	query := url.Values{}
	var last int64
	if after != nil {
		if *after <= 0 {
			return result, errors.New("usá un next_product_id de la página anterior u omití el cursor")
		}
		last = *after
		query.Set("after_product_id", strconv.FormatInt(last, 10))
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/pos-catalog", query, &result); err != nil {
		return result, err
	}
	if result.Currency != "ARS" || result.Products == nil || len(result.Products) > workspacePageSize {
		return result, invalidResponse
	}
	for _, p := range result.Products {
		if p.ID <= last || strings.TrimSpace(p.Name) == "" || p.PriceMinor <= 0 || p.Revision <= 0 {
			return result, invalidResponse
		}
		last = p.ID
	}
	if result.NextProductID != 0 && (len(result.Products) != workspacePageSize || result.NextProductID != last) {
		return result, invalidResponse
	}
	return result, nil
}

func (c *Client) StockMovements(ctx context.Context, entity, outlet, product int64, startDate string, days int) (StockMovements, error) {
	var result StockMovements
	start, err := parseReportDate(startDate)
	if err != nil {
		return result, err
	}
	if product <= 0 {
		return result, errors.New("indicá un product_id devuelto por gomar_list_stock")
	}
	if days < 1 || days > 31 {
		return result, errors.New("indicá una duración de 1 a 31 días")
	}
	query := url.Values{"start_date": {startDate}, "days": {strconv.Itoa(days)}}
	if err := c.getWorkspace(ctx, entity, outlet, "/stock/products/"+strconv.FormatInt(product, 10)+"/movements-summary", query, &result); err != nil {
		return result, err
	}
	if result.ProductID != product || result.Timezone != buenosAires || result.StartDate != startDate ||
		result.EndDateExclusive != start.AddDate(0, 0, days).Format(time.DateOnly) || len(result.Kinds) != 4 {
		return result, invalidResponse
	}
	for i, k := range []string{"recepcion", "merma", "conteo", "venta"} {
		item := result.Kinds[i]
		if item.Kind != k || item.MovementCount < 0 || (item.MovementCount == 0 && item.NetUnits != 0) {
			return result, invalidResponse
		}
	}
	return result, nil
}
