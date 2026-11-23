package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type outletDisplayLabelProposalArgs struct {
	outletArgs
	RequestID string                             `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Change    mcpclient.OutletDisplayLabelChange `json:"label_change"`
}

func addOutletDisplayLabelProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_outlet_display_label_change",
		Description: "Proponé cambiar solo el nombre para mostrar de un local activo. La misma sesión de Gomar revisa y aprueba. El ID, el acceso y los datos del local no cambian.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args outletDisplayLabelProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeOutletDisplayLabel(ctx, args.EntityID, args.OutletID, args.RequestID, args.Change)
		return nil, out, err
	})
}
