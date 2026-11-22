package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type posRenameProposalArgs struct {
	outletArgs
	RequestID string              `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Rename    mcpclient.POSRename `json:"rename"`
}

func addPOSRenameProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_pos_rename",
		Description: "Proponé cambiar sólo el nombre de un producto de caja después de leer su revisión actual. Requiere que la misma persona revise y apruebe en Gomar. No cambia precios, estado, ventas ni stock.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args posRenameProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposePOSRename(ctx, args.EntityID, args.OutletID, args.RequestID, args.Rename)
		return nil, out, err
	})
}
