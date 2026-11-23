package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stockProductSetupProposalArgs struct {
	outletArgs
	RequestID string                            `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Change    mcpclient.StockProductSetupChange `json:"stock_setup"`
}

func addStockProductSetupProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_stock_product_setup",
		Description: "Proponé configurar por primera vez el mínimo de stock de un producto de esta sucursal. La configuración queda desactivada. La misma sesión revisa y aprueba en Gomar. No registra movimientos ni activa el seguimiento.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args stockProductSetupProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeStockProductSetup(ctx, args.EntityID, args.OutletID, args.RequestID, args.Change)
		return nil, out, err
	})
}
