package preview

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPreviewThroughSDK(t *testing.T) {
	ctx := t.Context()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	serv, err := server().Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serv.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "preview-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != tool ||
		tools.Tools[0].Annotations == nil || !tools.Tools[0].Annotations.ReadOnlyHint {
		t.Fatalf("unexpected tools: %#v, %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{}})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("call result = %#v, %v", result, err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	want := summary{true, "2030-04-08", []currencyTotals{{"ARS", 3, 20950, 1, 12850}, {"USD", 1, 625, 0, 0}}}
	var got summary
	if !ok || json.Unmarshal([]byte(text.Text), &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("wrong preview result: %#v", result)
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`{"outlet":"otro"}`), json.RawMessage(`{"outlet":"uno","outlet":"dos"}`)} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: raw})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatal("unknown or repeated argument returned a result")
		}
	}
}

func TestNoArguments(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		if !hasNoArguments(raw) {
			t.Fatalf("rejected no arguments: %q", raw)
		}
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`{"k":1,"k":2}`), json.RawMessage(`{"k":`)} {
		if hasNoArguments(raw) {
			t.Fatalf("accepted invalid arguments: %q", raw)
		}
	}
}
