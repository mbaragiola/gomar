package main

import (
	"testing"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReservationPolicyNoticeToolAcceptsOnlyTypedHours(t *testing.T) {
	ctx := t.Context()
	server := liveServer(&mcpclient.Client{})
	addReservationPolicyNoticeProposalTool(server, &mcpclient.ProposalClient{})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	s, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "policy-notice-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	const name = "gomar_propose_reservation_policy_notice"
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tool := range list.Tools {
		if tool.Name == name && tool.Annotations.IdempotentHint {
			found = true
		}
	}
	if len(list.Tools) != 40 || !found {
		t.Fatal("expected 39 read tools and one named proposal")
	}
	for _, change := range []map[string]any{
		{}, {"notice_hours": nil}, {"notice_hours": 0}, {"notice_hours": 73}, {"notice_hours": 1.5},
		{"notice_hours": 24, "policy": "untyped"}, {"notice_hours": 24, "enabled": true},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{
			"entity_id": 4, "outlet_id": 3, "request_id": "e234c97f-6704-4b6e-a568-8a357d6fb572", "policy_change": change,
		}})
		if err != nil || !result.IsError {
			t.Fatal("invalid notice tool input passed", err)
		}
	}
}
