package main

import (
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDocumentNumberToolAdvertisesOnlyProposal(t *testing.T) {
	ctx := t.Context()
	server := liveServer(&mcpclient.Client{})
	addDocumentNumberProposalTool(server, &mcpclient.ProposalClient{})
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
	const name = "gomar_propose_document_number_change"
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var body *mcp.Tool
	for _, tool := range list.Tools {
		if tool.Name == name {
			body = tool
		}
	}
	if len(list.Tools) != 36 || body == nil || !body.Annotations.IdempotentHint {
		t.Fatal("expected 35 read tools and one named tool")
	}
	for _, update := range []map[string]any{
		{"number": nil}, {"number": 0}, {"number": 100000000}, {"number": 1.5},
		{"number": 777, "point_of_sale": 2}, {"number": 777, "expected_revision": 0},
	} {
		args := map[string]any{"entity_id": 4, "outlet_id": 3, "request_id": "e234c97f-6704-4b6e-a568-8a357d6fb572",
			"document_id": 12, "expected_revision": 1}
		for key, value := range update {
			args[key] = value
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || !result.IsError {
			t.Fatal("invalid document number tool input was accepted", err)
		}
	}
}
