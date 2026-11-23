package main

import (
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReservationSlotAddToolAdvertisesOnlyProposal(t *testing.T) {
	ctx := t.Context()
	server := mcp.NewServer(&mcp.Implementation{Name: "synthetic-pos-test", Version: "test"}, nil)
	addReservationSlotAddProposalTool(server, &mcpclient.ProposalClient{})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	s, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "local-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 1 || list.Tools[0].Name != "gomar_propose_reservation_slot_add" || !list.Tools[0].Annotations.IdempotentHint {
		t.Fatal(list, err)
	}
	for _, change := range []map[string]any{{}, {"start_at": nil}, {"start_at": "2030-05-21T01:30:01Z"}, {"start_at": "2030-05-20T22:30:00-03:00"}} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gomar_propose_reservation_slot_add", Arguments: map[string]any{
			"entity_id": 4, "outlet_id": 3, "request_id": "e234c97f-6704-4b6e-a568-8a357d6fb572", "slot_add": change,
		}})
		if err != nil || !result.IsError {
			t.Fatal(result, err)
		}
	}
}
