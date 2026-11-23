package mcpclient

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/mbaragiola/gomar/app/internal/posbarcode"
)

type POSProductDetails struct {
	POSProduct
	Currency string `json:"currency"`
}

func (c *Client) ReadPOSCatalogProduct(ctx context.Context, entity, outlet, id int64) (POSProductDetails, error) {
	if id <= 0 {
		return POSProductDetails{}, errors.New("indicá un product_id de gomar_pos_catalog de esta sucursal")
	}
	var wire struct {
		Named
		PriceMinor int64  `json:"price_minor"`
		Revision   int64  `json:"revision"`
		Active     *bool  `json:"active"`
		Currency   string `json:"currency"`
		Barcode    string `json:"barcode,omitempty"`
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/pos-catalog/"+strconv.FormatInt(id, 10), nil, &wire); err != nil {
		return POSProductDetails{}, err
	}
	if wire.ID != id || strings.TrimSpace(wire.Name) == "" || wire.PriceMinor <= 0 ||
		wire.Revision <= 0 || wire.Active == nil || wire.Currency != "ARS" ||
		wire.Barcode != "" && !posbarcode.Valid(wire.Barcode) {
		return POSProductDetails{}, invalidResponse
	}
	return POSProductDetails{
		POSProduct: POSProduct{Named: wire.Named, PriceMinor: wire.PriceMinor, Revision: wire.Revision, Active: *wire.Active, Barcode: wire.Barcode},
		Currency:   wire.Currency,
	}, nil
}
