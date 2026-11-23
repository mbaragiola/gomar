package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stockTrackingProposalArgs struct {
	outletArgs
	RequestID string                        `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Change    mcpclient.StockTrackingChange `json:"tracking_change"`
}

func addStockTrackingProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_stock_tracking_change",
		Description: "Proponé activar o desactivar el seguimiento de stock de un producto ya configurado en esta sucursal. La misma sesión debe revisar y aprobar en Gomar. No cambia el saldo ni el mínimo. Las ventas registradas mientras está desactivado no descuentan stock ni se recuperan al reactivar.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args stockTrackingProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeStockTrackingChange(ctx, args.EntityID, args.OutletID, args.RequestID, args.Change)
		return nil, out, err
	})
}
