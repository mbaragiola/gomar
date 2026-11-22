package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type posCreateProposalArgs struct {
	outletArgs
	RequestID string                     `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Product   mcpclient.POSProductCreate `json:"product"`
}

func addPOSCreateProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_pos_product_create",
		Description: "Proponé crear un producto de caja para una sucursal. La misma sesión tiene que revisar y aprobar en Gomar. No registra ventas, pagos ni movimientos de stock.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args posCreateProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposePOSProductCreate(ctx, args.EntityID, args.OutletID, args.RequestID, args.Product)
		return nil, out, err
	})
}
