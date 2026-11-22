package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type POSProductCreate struct {
	Name       string `json:"name" jsonschema:"Nombre del producto de caja; de 1 a 120 caracteres."`
	PriceMinor int64  `json:"price_minor" jsonschema:"Precio entero en centavos de ARS; de 1 a 100000000."`
	Active     *bool  `json:"active" jsonschema:"Disponibilidad explícita: true o false."`
}

func (c *ProposalClient) ProposePOSProductCreate(ctx context.Context, entity, outlet int64, requestID string, product POSProductCreate) (ProposalReceipt, error) {
	product.Name = strings.TrimSpace(product.Name)
	if entity <= 0 || outlet <= 0 || !proposalRequestID.MatchString(requestID) ||
		product.Name == "" || !utf8.ValidString(product.Name) || utf8.RuneCountInString(product.Name) > 120 ||
		strings.ContainsRune(product.Name, 0) || product.PriceMinor < 1 || product.PriceMinor > 100000000 || product.Active == nil {
		return ProposalReceipt{}, errors.New("indicá la empresa, sucursal, nombre, precio en centavos, disponibilidad y request_id UUIDv4")
	}
	payload, err := json.Marshal(struct {
		RequestID string           `json:"request_id"`
		Product   POSProductCreate `json:"product"`
	}{requestID, product})
	if err != nil {
		return ProposalReceipt{}, errors.New("no se pudo preparar la propuesta")
	}
	return c.postProposal(ctx, entity, outlet, "/pos-create-proposals", payload)
}
