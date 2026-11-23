package mcpclient

import (
	"context"
	"errors"
	"strconv"
)

type PayablePreflight struct {
	Eligible   bool   `json:"eligible"`
	Blocker    string `json:"blocker,omitempty"`
	HandoffURL string `json:"handoff_url,omitempty"`
}

// PayableEligibilityHandoffURL contains only the canonical browser locator.
// It is never an approval, bearer, or redirect to the native write.
func PayableEligibilityHandoffURL(entity, outlet, document int64) string {
	const maxID = (1 << 53) - 1
	if entity <= 0 || outlet <= 0 || document <= 0 || entity > maxID || outlet > maxID || document > maxID {
		return ""
	}
	return "/#alta-cuenta=" + strconv.FormatInt(entity, 10) + ":" + strconv.FormatInt(outlet, 10) + ":" + strconv.FormatInt(document, 10)
}

func (c *Client) PayablePreflight(ctx context.Context, entity, outlet, document int64) (PayablePreflight, error) {
	if document <= 0 {
		return PayablePreflight{}, errors.New("indicá el document_id de un comprobante de esta sucursal")
	}
	var wire struct {
		Eligible   *bool   `json:"eligible"`
		Blocker    *string `json:"blocker"`
		HandoffURL *string `json:"handoff_url"`
	}
	path := "/documents/" + strconv.FormatInt(document, 10) + "/payable-preflight"
	if err := c.getWorkspace(ctx, entity, outlet, path, nil, &wire); err != nil {
		return PayablePreflight{}, err
	}
	if wire.Eligible == nil {
		return PayablePreflight{}, invalidResponse
	}
	if wire.HandoffURL != nil {
		expected := PayableEligibilityHandoffURL(entity, outlet, document)
		if !*wire.Eligible || wire.Blocker != nil || expected == "" || *wire.HandoffURL != expected {
			return PayablePreflight{}, invalidResponse
		}
		return PayablePreflight{Eligible: true, HandoffURL: expected}, nil
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
