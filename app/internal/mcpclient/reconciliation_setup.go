package mcpclient

import (
	"context"
	"errors"
	"strconv"
)

type ReconciliationSetup struct {
	ID                         int64  `json:"id"`
	Revision                   int64  `json:"revision"`
	Status                     string `json:"status"`
	MatchReference             bool   `json:"match_reference"`
	HasPaymentsSource          bool   `json:"has_payments_source"`
	HasCashFlowSource          bool   `json:"has_cash_flow_source"`
	ReferenceMappingIncomplete bool   `json:"reference_mapping_incomplete"`
	UnsupportedSourceSetup     bool   `json:"unsupported_source_setup"`
	OverRowLimit               bool   `json:"over_row_limit"`
}

func (c *Client) ReadReconciliationSetup(ctx context.Context, entity, outlet, run int64) (ReconciliationSetup, error) {
	if run <= 0 {
		return ReconciliationSetup{}, errors.New("indicá un run_id devuelto por gomar_list_reconciliation_runs")
	}
	var wire struct {
		ReconciliationSetup
		MatchReference             *bool `json:"match_reference"`
		HasPaymentsSource          *bool `json:"has_payments_source"`
		HasCashFlowSource          *bool `json:"has_cash_flow_source"`
		ReferenceMappingIncomplete *bool `json:"reference_mapping_incomplete"`
		UnsupportedSourceSetup     *bool `json:"unsupported_source_setup"`
		OverRowLimit               *bool `json:"over_row_limit"`
	}
	if err := c.getWorkspace(ctx, entity, outlet, "/reconciliation-runs/"+strconv.FormatInt(run, 10)+"/setup", nil, &wire); err != nil {
		return ReconciliationSetup{}, err
	}
	out := wire.ReconciliationSetup
	if out.ID != run || out.Revision <= 0 || (out.Status != "draft" && out.Status != "review" && out.Status != "closed") ||
		wire.MatchReference == nil || wire.HasPaymentsSource == nil || wire.HasCashFlowSource == nil ||
		wire.ReferenceMappingIncomplete == nil || wire.UnsupportedSourceSetup == nil || wire.OverRowLimit == nil ||
		(!*wire.MatchReference && *wire.ReferenceMappingIncomplete) {
		return ReconciliationSetup{}, invalidResponse
	}
	out.MatchReference = *wire.MatchReference
	out.HasPaymentsSource = *wire.HasPaymentsSource
	out.HasCashFlowSource = *wire.HasCashFlowSource
	out.ReferenceMappingIncomplete = *wire.ReferenceMappingIncomplete
	out.UnsupportedSourceSetup = *wire.UnsupportedSourceSetup
	out.OverRowLimit = *wire.OverRowLimit
	return out, nil
}
