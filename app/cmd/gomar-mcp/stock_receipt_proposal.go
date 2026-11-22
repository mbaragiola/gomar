package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stockReceiptProposalArgs struct {
	outletArgs
	RequestID string                 `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Receipt   mcpclient.StockReceipt `json:"receipt"`
}

func addStockReceiptProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_stock_receipt",
		Description: "Proponé registrar una entrada de stock para un producto activo. Sumá sólo la cantidad recibida. No reemplaza el saldo ni modifica precios o ventas. La persona que hizo la propuesta debe aprobar la entrada en Gomar.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args stockReceiptProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeStockReceipt(ctx, args.EntityID, args.OutletID, args.RequestID, args.Receipt)
		return nil, out, err
	})
}
