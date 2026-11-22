package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type wikiEditProposalArgs struct {
	outletArgs
	RequestID string             `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar usá el mismo ID y contenido."`
	Edit      mcpclient.WikiEdit `json:"edit"`
}

func addWikiEditProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_wiki_edit",
		Description: "Proponé cambiar una página existente después de leer su versión actual. No modifica ni publica la página. Quien hizo la propuesta revisa el texto anterior y el nuevo, y confirma el cambio en Gomar.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args wikiEditProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeWikiEdit(ctx, args.EntityID, args.OutletID, args.RequestID, args.Edit)
		return nil, out, err
	})
}
