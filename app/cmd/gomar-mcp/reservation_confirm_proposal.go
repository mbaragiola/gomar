package main

import (
	"context"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reservationConfirmationArgs struct {
	outletArgs
	RequestID    string                            `json:"request_id" jsonschema:"UUIDv4 en minúsculas. Al reintentar, usá el mismo ID y contenido."`
	Confirmation mcpclient.ReservationConfirmation `json:"confirmation"`
}

func addReservationConfirmProposalTool(server *mcp.Server, client *mcpclient.ProposalClient) {
	no := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gomar_propose_reservation_confirmation",
		Description: "Proponé confirmar una solicitud pendiente de reserva. La misma sesión revisa la fecha, cantidad de personas y mesa exacta en Gomar antes de confirmar. No confirma por chat ni envía mensajes a clientes.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no, IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args reservationConfirmationArgs) (*mcp.CallToolResult, mcpclient.ProposalReceipt, error) {
		out, err := client.ProposeReservationConfirmation(ctx, args.EntityID, args.OutletID, args.RequestID, args.Confirmation)
		return nil, out, err
	})
}
