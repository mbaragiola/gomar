package main

import (
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOutletDisplayLabelToolAdvertisesOnlyProposal(t *testing.T) {
	ctx := t.Context()
	server := mcp.NewServer(&mcp.Implementation{Name: "synthetic-pos-test", Version: "test"}, nil)
	addOutletDisplayLabelProposalTool(server, &mcpclient.ProposalClient{})
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
	if err != nil || len(list.Tools) != 1 || list.Tools[0].Name != "gomar_propose_outlet_display_label_change" || !list.Tools[0].Annotations.IdempotentHint {
		t.Fatal(list, err)
	}
	for _, change := range []map[string]any{{}, {"expected_name": "Centro"}, {"expected_name": "Centro", "name": "Centro"}, {"expected_name": "Centro", "name": " Centro nuevo"}, {"expected_name": "Centro", "name": "Nuevo", "active": false}} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gomar_propose_outlet_display_label_change", Arguments: map[string]any{
			"entity_id": 4, "outlet_id": 3, "request_id": "e234c97f-6704-4b6e-a568-8a357d6fb572", "label_change": change,
		}})
		if err != nil || !result.IsError {
			t.Fatal(result, err)
		}
	}
}
