package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type wikiCreateArgs struct {
	outletArgs
	RequestID string `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Para reintentar la misma propuesta usá el mismo ID y contenido."`
	Title     string `json:"title" jsonschema:"Título de la nueva página, hasta 120 caracteres."`
	Body      string `json:"body" jsonschema:"Contenido en texto plano, hasta 12000 caracteres. No incluyas credenciales ni datos privados ajenos a la wiki."`
}

func addWikiProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_wiki_page",
		Description: "Dejá una propuesta de página nueva para la wiki de una sucursal autorizada. No publica ni modifica páginas. Después, la persona que hizo la propuesta debe revisarla y aprobarla en Gomar. No le pidas su clave en este chat.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args wikiCreateArgs) (*mcp.CallToolResult, mcpclient.WikiCreateReceipt, error) {
		out, err := client.ProposeWikiCreate(ctx, args.EntityID, args.OutletID, args.RequestID, args.Title, args.Body)
		return nil, out, err
	})
}
