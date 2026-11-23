package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type ReservationSlotAddChange struct {
	StartAt string `json:"start_at" jsonschema:"Hora UTC en RFC3339 canónico (AAAA-MM-DDTHH:MM:00Z), en un inicio de turno válido para Buenos Aires."`
}

func (c *ProposalClient) ProposeReservationSlotAdd(ctx context.Context, entity, outlet int64, requestID string, change ReservationSlotAddChange) (ProposalReceipt, error) {
	t, err := time.Parse(time.RFC3339, change.StartAt)
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) ||
		err != nil || t.UTC().Format(time.RFC3339) != change.StartAt || t.Second() != 0 {
		return ProposalReceipt{}, errors.New("indicá empresa, local, inicio canónico UTC con segundos 00 y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string                   `json:"request_id"`
		Change    ReservationSlotAddChange `json:"slot_add"`
	}{requestID, change})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/reservation-slot-add-proposals", payload)
}
