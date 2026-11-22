package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type posCatalogProposalArgs[C any] struct {
	outletArgs
	RequestID string `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Change    C      `json:"change"`
}

func addPOSCatalogProposal[C any](server *mcp.Server, name, description string, propose func(context.Context, int64, int64, string, C) (mcpclient.ProposalReceipt, error)) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args posCatalogProposalArgs[C]) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := propose(ctx, args.EntityID, args.OutletID, args.RequestID, args.Change)
		return nil, out, err
	})
}

func addPOSPriceProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	addPOSCatalogProposal(server, "gomar_propose_pos_price",
		"Proponé cambiar sólo el precio de un producto de caja. Queda pendiente hasta que la sesión de origen revise y confirme en Gomar. No cambia ventas previas, nombre, disponibilidad ni stock.", client.ProposePOSPrice)
}

func addPOSAvailabilityProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	addPOSCatalogProposal(server, "gomar_propose_pos_availability",
		"Proponé activar o desactivar un producto para ventas futuras. Requiere aprobación en Gomar desde la misma sesión. No cambia precio, nombre, stock ni ventas anteriores.", client.ProposePOSAvailability)
}
