package main

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Only successful dispatch identifies a registered tool. Protocol errors can
// contain a caller-supplied name, so their diagnostics omit the name entirely.
func toolDiagnostics(out io.Writer) mcp.Middleware {
	logger := slog.New(slog.NewJSONHandler(out, nil))
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			call, ok := request.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok {
				return next(ctx, method, request)
			}
			start := time.Now()
			result, err := next(ctx, method, request)
			fields := []any{"call_id", rand.Text(), "duration_ms", time.Since(start).Milliseconds()}
			outcome := "protocol_error"
			if response, ok := result.(*mcp.CallToolResult); err == nil && ok && response != nil && call.Params != nil {
				fields = append(fields, "tool", call.Params.Name)
				outcome = "ok"
				if response.IsError {
					outcome = "tool_error"
					if len(response.Content) == 1 {
						if msg, ok := response.Content[0].(*mcp.TextContent); ok {
							for _, code := range []string{"credential_invalid", "scope_unavailable", "proposal_conflict", "temporarily_unavailable"} {
								if strings.HasSuffix(msg.Text, " [gomar:"+code+"]") {
									fields = append(fields, "error_code", code)
									break
								}
							}
						}
					}
				}
			}
			logger.Info("gomar_mcp_call", append(fields, "outcome", outcome)...)
			return result, err
		}
	}
}
