package mcpclient

import (
	"context"
	"errors"
	"strconv"
)

type StockProductDetails struct {
	ID             int64  `json:"id"`
	OutletEnabled  bool   `json:"outlet_enabled"`
	ProductEnabled bool   `json:"product_enabled"`
	Minimum        int64  `json:"minimum"`
	Balance        int64  `json:"balance"`
	Alert          string `json:"alert,omitempty"`
}

func (c *Client) ReadStockProduct(ctx context.Context, entity, outlet, id int64) (StockProductDetails, error) {
	var result StockProductDetails
	if id <= 0 {
		return result, errors.New("indicá un product_id de esta sucursal")
	}
	var wire struct {
		ID             int64  `json:"id"`
		OutletEnabled  *bool  `json:"outlet_enabled"`
		ProductEnabled *bool  `json:"product_enabled"`
		Minimum        *int64 `json:"minimum"`
		Balance        *int64 `json:"balance"`
		Alert          string `json:"alert"`
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/stock/"+strconv.FormatInt(id, 10), nil, &wire); err != nil {
		return result, err
	}
	if wire.ID != id || wire.OutletEnabled == nil || wire.ProductEnabled == nil ||
		wire.Minimum == nil || *wire.Minimum < 0 || *wire.Minimum > 1_000_000 || wire.Balance == nil {
		return result, invalidResponse
	}
	var alert string
	switch {
	case *wire.OutletEnabled && *wire.ProductEnabled && *wire.Balance < 0:
		alert = "negative"
	case *wire.OutletEnabled && *wire.ProductEnabled && *wire.Balance <= *wire.Minimum:
		alert = "low"
	}
	if wire.Alert != alert {
		return result, invalidResponse
	}
	return StockProductDetails{ID: id, OutletEnabled: *wire.OutletEnabled, ProductEnabled: *wire.ProductEnabled,
		Minimum: *wire.Minimum, Balance: *wire.Balance, Alert: alert}, nil
}
