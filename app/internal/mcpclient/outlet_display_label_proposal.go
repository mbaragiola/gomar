package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

type OutletDisplayLabelChange struct {
	ExpectedName string `json:"expected_name" jsonschema:"Nombre actual exacto del local según gomar_list_outlets."`
	Name         string `json:"name" jsonschema:"Nuevo nombre para mostrar, de 1 a 120 caracteres, sin espacios en los extremos."`
}

func validOutletDisplayLabel(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 120 &&
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError }) == -1
}

func (c *ProposalClient) ProposeOutletDisplayLabel(ctx context.Context, entity, outlet int64, requestID string, change OutletDisplayLabelChange) (ProposalReceipt, error) {
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) ||
		!validOutletDisplayLabel(change.ExpectedName) || !validOutletDisplayLabel(change.Name) || change.ExpectedName == change.Name {
		return ProposalReceipt{}, errors.New("indicá empresa, local, nombre actual y nuevo válidos y distintos, y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string                   `json:"request_id"`
		Change    OutletDisplayLabelChange `json:"label_change"`
	}{requestID, change})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/outlet-display-label-proposals", payload)
}
