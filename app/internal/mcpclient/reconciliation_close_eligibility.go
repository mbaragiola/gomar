package mcpclient

import (
	"context"
	"errors"
	"strconv"
)

type ReconciliationCloseEligibility struct {
	Eligible   bool   `json:"eligible"`
	Blocker    string `json:"blocker,omitempty"`
	HandoffURL string `json:"handoff_url,omitempty"`
}

// ReconciliationCloseHandoffURL returns only the browser fragment for safe
// numeric IDs. A link never grants permission or acknowledges a close.
func ReconciliationCloseHandoffURL(entity, outlet, run int64) string {
	const maxID = (1 << 53) - 1
	if entity <= 0 || outlet <= 0 || run <= 0 || entity > maxID || outlet > maxID || run > maxID {
		return ""
	}
	return "/#conciliacion=" + strconv.FormatInt(entity, 10) + ":" + strconv.FormatInt(outlet, 10) + ":" + strconv.FormatInt(run, 10)
}

func (c *Client) ReadReconciliationCloseEligibility(ctx context.Context, entity, outlet, run int64) (ReconciliationCloseEligibility, error) {
	if run <= 0 {
		return ReconciliationCloseEligibility{}, errors.New("indicá el run_id devuelto por gomar_list_reconciliation_runs")
	}
	var wire struct {
		Eligible   *bool   `json:"eligible"`
		Blocker    *string `json:"blocker"`
		HandoffURL *string `json:"handoff_url"`
	}
	path := "/reconciliation-runs/" + strconv.FormatInt(run, 10) + "/close-eligibility"
	if err := c.getWorkspace(ctx, entity, outlet, path, nil, &wire); err != nil {
		return ReconciliationCloseEligibility{}, err
	}
	if wire.Eligible == nil {
		return ReconciliationCloseEligibility{}, invalidResponse
	}
	var handoff string
	if wire.HandoffURL != nil {
		expected := ReconciliationCloseHandoffURL(entity, outlet, run)
		if expected == "" || *wire.HandoffURL != expected || wire.Blocker != nil && *wire.Blocker == "not_in_review" {
			return ReconciliationCloseEligibility{}, invalidResponse
		}
		handoff = expected
	}
	if *wire.Eligible && wire.Blocker == nil {
		return ReconciliationCloseEligibility{Eligible: true, HandoffURL: handoff}, nil
	}
	if !*wire.Eligible && wire.Blocker != nil {
		switch *wire.Blocker {
		case "not_in_review", "unsupported_review_data":
			return ReconciliationCloseEligibility{Blocker: *wire.Blocker, HandoffURL: handoff}, nil
		}
	}
	return ReconciliationCloseEligibility{}, invalidResponse
}
