package main

import (
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStockProductSetupToolAdvertisesOnlyProposal(t *testing.T) {
	ctx := t.Context()
	server := mcp.NewServer(&mcp.Implementation{Name: "synthetic-pos-test", Version: "test"}, nil)
	addStockProductSetupProposalTool(server, &mcpclient.ProposalClient{})
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
	if err != nil || len(list.Tools) != 1 || list.Tools[0].Name != "gomar_propose_stock_product_setup" || !list.Tools[0].Annotations.IdempotentHint {
		t.Fatal(list, err)
	}
	for _, change := range []map[string]any{{"product_id": 7, "expected_revision": 2}, {"product_id": 7, "expected_revision": 2, "minimum": nil}, {"product_id": 7, "expected_revision": 2, "minimum": 1.5}} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gomar_propose_stock_product_setup", Arguments: map[string]any{
			"entity_id": 4, "outlet_id": 3, "request_id": "e234c97f-6704-4b6e-a568-8a357d6fb572", "stock_setup": change,
		}})
		if err != nil || !result.IsError {
			t.Fatal(result, err)
		}
	}
}
