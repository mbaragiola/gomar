package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
)

type ReconciliationDraft struct {
	MatchReference *bool `json:"match_reference" jsonschema:"Elegí expresamente si se debe comparar la referencia: true o false."`
}

func (c *ProposalClient) ProposeReconciliationDraft(ctx context.Context, entity, outlet int64, requestID string, draft ReconciliationDraft) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) || draft.MatchReference == nil {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, match_reference y un request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string              `json:"request_id"`
		Draft     ReconciliationDraft `json:"draft"`
	}{requestID, draft})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/reconciliation-draft-proposals", payload)
}
