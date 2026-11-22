package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode/utf8"
)

type POSRename struct {
	ProductID        int64  `json:"product_id" jsonschema:"ID de un producto del catálogo de esta sucursal."`
	ExpectedRevision int64  `json:"expected_revision" jsonschema:"Revisión actual devuelta por gomar_pos_catalog."`
	Name             string `json:"name" jsonschema:"Nuevo nombre, hasta 120 caracteres. No cambia el precio, la disponibilidad ni el stock."`
}

func (c *ProposalClient) ProposePOSRename(ctx context.Context, entity, outlet int64, requestID string, rename POSRename) (ProposalReceipt, error) {
	if entity < 1 || outlet < 1 || !proposalRequestID.MatchString(requestID) ||
		rename.ProductID < 1 || rename.ExpectedRevision < 1 || rename.ExpectedRevision == math.MaxInt64 {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, producto, revisión y un request_id UUIDv4")
	}
	rename.Name = strings.TrimSpace(rename.Name)
	if rename.Name == "" || !utf8.ValidString(rename.Name) ||
		utf8.RuneCountInString(rename.Name) > 120 || strings.ContainsRune(rename.Name, 0) {
		return ProposalReceipt{}, errors.New("indicá un nombre de producto de hasta 120 caracteres")
	}
	payload, err := json.Marshal(struct {
		RequestID string    `json:"request_id"`
		Rename    POSRename `json:"rename"`
	}{requestID, rename})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/pos-rename-proposals", payload)
}
