package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
)

type ReservationPolicyNoticeChange struct {
	NoticeHours int `json:"notice_hours" jsonschema:"Cantidad entera de horas de aviso previo: 1 a 72."`
}

func (c *ProposalClient) ProposeReservationPolicyNotice(ctx context.Context, entity, outlet int64, requestID string, change ReservationPolicyNoticeChange) (ProposalReceipt, error) {
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) || change.NoticeHours < 1 || change.NoticeHours > 72 {
		return ProposalReceipt{}, errors.New("indicá empresa, local, entre 1 y 72 horas enteras y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string                        `json:"request_id"`
		Change    ReservationPolicyNoticeChange `json:"policy_change"`
	}{requestID, change})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/reservation-policy-notice-proposals", payload)
}
