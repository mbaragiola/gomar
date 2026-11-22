package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stockAdjustmentProposalArgs struct {
	outletArgs
	RequestID  string                    `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Adjustment mcpclient.StockAdjustment `json:"adjustment"`
}

func addStockAdjustmentProposal(server *mcp.Server, name, description string, propose func(context.Context, int64, int64, string, mcpclient.StockAdjustment) (mcpclient.ProposalReceipt, error)) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args stockAdjustmentProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := propose(ctx, args.EntityID, args.OutletID, args.RequestID, args.Adjustment)
		return nil, out, err
	})
}

func addStockWasteProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	addStockAdjustmentProposal(server, "gomar_propose_stock_waste",
		"Proponé registrar una merma por una cantidad positiva de un producto. No cambia el stock hasta que la sesión de origen confirme el saldo anterior y el nuevo en Gomar.", client.ProposeStockWaste)
}

func addStockPhysicalCountProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	addStockAdjustmentProposal(server, "gomar_propose_stock_physical_count",
		"Proponé registrar las unidades contadas físicamente para un producto. Cero requiere indicarlo explícitamente. No cambia el stock hasta que la sesión de origen confirme en Gomar; un movimiento entre tanto invalida la propuesta.", client.ProposeStockPhysicalCount)
}
