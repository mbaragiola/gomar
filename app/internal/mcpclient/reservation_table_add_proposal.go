package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
)

type ReservationTableAddChange struct {
	TableNumber int `json:"table_number" jsonschema:"Número de mesa de 1 a 9999; se guarda como Mesa N."`
	Capacity    int `json:"capacity" jsonschema:"Capacidad entera de 1 a 12 personas."`
}

func (c *ProposalClient) ProposeReservationTableAdd(ctx context.Context, entity, outlet int64, requestID string, change ReservationTableAddChange) (ProposalReceipt, error) {
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) ||
		change.TableNumber < 1 || change.TableNumber > 9999 || change.Capacity < 1 || change.Capacity > 12 {
		return ProposalReceipt{}, errors.New("indicá empresa, local, número de mesa de 1 a 9999, capacidad de 1 a 12 y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string                    `json:"request_id"`
		Change    ReservationTableAddChange `json:"table_add"`
	}{requestID, change})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/reservation-table-add-proposals", payload)
}
