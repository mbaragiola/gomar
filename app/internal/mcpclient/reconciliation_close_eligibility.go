package mcpclient

import (
	"context"
	"errors"
	"strconv"
)

type ReconciliationCloseEligibility struct {
	Eligible bool   `json:"eligible"`
	Blocker  string `json:"blocker,omitempty"`
}

func (c *Client) ReadReconciliationCloseEligibility(ctx context.Context, entity, outlet, run int64) (ReconciliationCloseEligibility, error) {
	if run <= 0 {
		return ReconciliationCloseEligibility{}, errors.New("indicá el run_id devuelto por gomar_list_reconciliation_runs")
	}
	var wire struct {
		Eligible *bool   `json:"eligible"`
		Blocker  *string `json:"blocker"`
	}
	path := "/reconciliation-runs/" + strconv.FormatInt(run, 10) + "/close-eligibility"
	if err := c.getWorkspace(ctx, entity, outlet, path, nil, &wire); err != nil {
		return ReconciliationCloseEligibility{}, err
	}
	if wire.Eligible == nil {
		return ReconciliationCloseEligibility{}, invalidResponse
	}
	if *wire.Eligible && wire.Blocker == nil {
		return ReconciliationCloseEligibility{Eligible: true}, nil
	}
	if !*wire.Eligible && wire.Blocker != nil {
		switch *wire.Blocker {
		case "not_in_review", "unsupported_review_data":
			return ReconciliationCloseEligibility{Blocker: *wire.Blocker}, nil
		}
	}
	return ReconciliationCloseEligibility{}, invalidResponse
}
