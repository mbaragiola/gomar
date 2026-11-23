package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reservationPolicyNoticeProposalArgs struct {
	outletArgs
	RequestID string                                  `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Change    mcpclient.ReservationPolicyNoticeChange `json:"policy_change"`
}

func addReservationPolicyNoticeProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_reservation_policy_notice",
		Description: "Proponé texto informativo de aviso previo para un local de reservas ya configurado y desactivado. Usá entre 1 y 72 horas enteras. Solo la misma sesión de Gomar puede ver y aprobar el texto; no habilita reservas, cancela solicitudes ni envía avisos.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args reservationPolicyNoticeProposalArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeReservationPolicyNotice(ctx, args.EntityID, args.OutletID, args.RequestID, args.Change)
		return nil, out, err
	})
}
