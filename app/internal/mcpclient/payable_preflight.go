package mcpclient

import (
	"context"
	"errors"
	"strconv"
)

type PayablePreflight struct {
	Eligible bool   `json:"eligible"`
	Blocker  string `json:"blocker,omitempty"`
}

func (c *Client) PayablePreflight(ctx context.Context, entity, outlet, document int64) (PayablePreflight, error) {
	if document <= 0 {
		return PayablePreflight{}, errors.New("indicá el document_id de un comprobante de esta sucursal")
	}
	var wire struct {
		Eligible *bool   `json:"eligible"`
		Blocker  *string `json:"blocker"`
	}
	path := "/documents/" + strconv.FormatInt(document, 10) + "/payable-preflight"
	if err := c.getWorkspace(ctx, entity, outlet, path, nil, &wire); err != nil {
		return PayablePreflight{}, err
	}
	if wire.Eligible == nil {
		return PayablePreflight{}, invalidResponse
	}
	if *wire.Eligible && wire.Blocker == nil {
		return PayablePreflight{Eligible: true}, nil
	}
	if !*wire.Eligible && wire.Blocker != nil {
		switch *wire.Blocker {
		case "already_posted", "not_reviewed", "not_invoice", "nonpositive_total", "total_out_of_range", "supplier_mismatch":
			return PayablePreflight{Blocker: *wire.Blocker}, nil
		}
	}
	return PayablePreflight{}, invalidResponse
}
