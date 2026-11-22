package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
)

var reservationStaffReference = regexp.MustCompile(`^[0-9a-f]{32}$`)

type ReservationConfirmation struct {
	Reference string `json:"reservation_reference" jsonschema:"Referencia interna exacta de una solicitud de reserva pendiente."`
}

type ReservationCancellation struct {
	Reference string `json:"reservation_reference" jsonschema:"Referencia interna exacta de una reserva futura pendiente o confirmada."`
}

func (c *ProposalClient) ProposeReservationConfirmation(ctx context.Context, entity, outlet int64, requestID string, confirmation ReservationConfirmation) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) ||
		!reservationStaffReference.MatchString(confirmation.Reference) {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, referencia de reserva y un request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID    string                  `json:"request_id"`
		Confirmation ReservationConfirmation `json:"confirmation"`
	}{requestID, confirmation})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/reservation-confirm-proposals", payload)
}

func (c *ProposalClient) ProposeReservationCancellation(ctx context.Context, entity, outlet int64, requestID string, cancellation ReservationCancellation) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) ||
		!reservationStaffReference.MatchString(cancellation.Reference) {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, referencia de reserva y un request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID    string                  `json:"request_id"`
		Cancellation ReservationCancellation `json:"cancellation"`
	}{requestID, cancellation})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/reservation-cancel-proposals", payload)
}
