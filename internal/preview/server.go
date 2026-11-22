package preview

import (
	"context"
	"encoding/json"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const tool = "get_payables_summary"

func server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "gomar-preview", Version: "0.1.0"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	closedWorld := false
	s.AddTool(&mcp.Tool{
		Name:        tool,
		Description: "Resume facturas pendientes y vencidas en datos inventados. Separa ARS y USD y devuelve centavos. No modifica nada.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, readSummary)
	return s
}

func readSummary(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if req.Params == nil || !hasNoArguments(req.Params.Arguments) {
		return message("No se admiten argumentos en esta demo.", true), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report, err := demoSummary()
	if err != nil {
		return message("No se pudo preparar el ejemplo.", true), nil
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return message("No se pudo preparar el ejemplo.", true), nil
	}
	return message(string(raw), false), nil
}

func message(text string, failed bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: failed}
}

func hasNoArguments(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var args map[string]json.RawMessage
	return json.Unmarshal(raw, &args) == nil && args != nil && len(args) == 0
}

// Serve uses the SDK over the caller's input and output. It opens no listener.
func Serve(ctx context.Context, in io.ReadCloser, out io.WriteCloser) error {
	return server().Run(ctx, &mcp.IOTransport{Reader: in, Writer: out, MaxLineLength: 16 * 1024})
}
