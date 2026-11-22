package main

import (
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPOSCreateToolAdvertisesOnlyProposal(t *testing.T) {
	ctx := t.Context()
	server := mcp.NewServer(&mcp.Implementation{Name: "synthetic-pos-test", Version: "test"}, nil)
	addPOSCreateProposalTool(server, &mcpclient.ProposalClient{})
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
	if err != nil || len(list.Tools) != 1 || list.Tools[0].Name != "gomar_propose_pos_product_create" || !list.Tools[0].Annotations.IdempotentHint {
		t.Fatal(list, err)
	}
	for _, product := range []map[string]any{{"name": "Pan", "price_minor": 123}, {"name": "Pan", "price_minor": 123, "active": nil}} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gomar_propose_pos_product_create", Arguments: map[string]any{
			"entity_id": 4, "outlet_id": 3, "request_id": "e234c97f-6704-4b6e-a568-8a357d6fb572", "product": product,
		}})
		if err != nil || !result.IsError {
			t.Fatal(result, err)
		}
	}
}
