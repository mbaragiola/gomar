// Package gomarmcp serves the read-only Gomar chat interface over MCP stdio.
package gomarmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/mbaragiola/gomar/app/internal/payablechat"
)

const (
	latestProtocol = "2025-11-25"
	toolName       = "get_payables_summary"
	maxFrame       = 16 * 1024
	readTimeout    = 2 * time.Second
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var integerID = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func validID(id json.RawMessage) bool {
	return len(id) > 0 && (id[0] == '"' || integerID.Match(id))
}

func errorResponse(id json.RawMessage, code int, message string) response {
	if !validID(id) {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, message}}
}

func object(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

func absentOrEmpty(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	return object(raw) && json.Unmarshal(raw, &fields) == nil && len(fields) == 0
}

func supportedVersion(v string) string {
	switch v {
	case "2025-11-25", "2025-06-18", "2025-03-26":
		return v
	default:
		return latestProtocol
	}
}

// Serve handles one MCP connection. The caller controls stream lifetime.
// Close the input on shutdown to interrupt an idle read. Summary readers
// must respect their context; Serve does not leave abandoned work running.
func Serve(ctx context.Context, in io.Reader, out io.Writer, reader payablechat.Reader) error {
	if reader == nil {
		return errors.New("missing payable summary reader")
	}
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), maxFrame+1)
	encoder := json.NewEncoder(out)
	var initialized, ready bool
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !scan.Scan() {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scan.Bytes()
		var r request
		if !utf8.Valid(line) || !json.Valid(line) {
			if err := encoder.Encode(errorResponse(nil, -32700, "Parse error")); err != nil {
				return err
			}
			continue
		}
		if !object(line) || json.Unmarshal(line, &r) != nil || r.JSONRPC != "2.0" || r.Method == "" ||
			(len(r.ID) > 0 && !validID(r.ID)) {
			if err := encoder.Encode(errorResponse(nil, -32600, "Invalid request")); err != nil {
				return err
			}
			continue
		}
		if len(r.ID) == 0 {
			if r.Method == "notifications/initialized" && initialized {
				ready = true
			}
			continue // Notifications receive no response.
		}

		result := response{JSONRPC: "2.0", ID: r.ID}
		switch r.Method {
		case "ping":
			result.Result = struct{}{}
		case "initialize":
			var p struct {
				Protocol     string          `json:"protocolVersion"`
				Capabilities json.RawMessage `json:"capabilities"`
				Client       struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"clientInfo"`
			}
			if initialized {
				result = errorResponse(r.ID, -32600, "Already initialized")
			} else if !object(r.Params) || json.Unmarshal(r.Params, &p) != nil ||
				p.Protocol == "" || !object(p.Capabilities) || p.Client.Name == "" || p.Client.Version == "" {
				result = errorResponse(r.ID, -32602, "Invalid initialize params")
			} else {
				initialized = true
				result.Result = map[string]any{
					"protocolVersion": supportedVersion(p.Protocol),
					"capabilities":    map[string]any{"tools": map[string]bool{"listChanged": false}},
					"serverInfo":      map[string]string{"name": "gomar-synthetic", "version": "0.1.0"},
				}
			}
		case "tools/list":
			if !ready {
				result = errorResponse(r.ID, -32002, "Server not ready")
				break
			}
			var params struct {
				Cursor string `json:"cursor"`
			}
			if len(r.Params) != 0 && (!object(r.Params) || json.Unmarshal(r.Params, &params) != nil || params.Cursor != "") {
				result = errorResponse(r.ID, -32602, "No pagination for this tool list")
				break
			}
			result.Result = map[string]any{"tools": []map[string]any{{
				"name":        toolName,
				"description": "Resume saldos pendientes y vencidos del alcance sintético fijo. Separa monedas y devuelve centavos.",
				"inputSchema": map[string]any{"type": "object", "properties": struct{}{}, "additionalProperties": false},
				"annotations": map[string]bool{"readOnlyHint": true, "openWorldHint": false},
			}}}
		case "tools/call":
			if !ready {
				result = errorResponse(r.ID, -32002, "Server not ready")
				break
			}
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if !object(r.Params) || json.Unmarshal(r.Params, &p) != nil || p.Name == "" {
				result = errorResponse(r.ID, -32602, "Invalid tool call params")
			} else if p.Name != toolName {
				result = errorResponse(r.ID, -32602, "Unknown tool")
			} else if !absentOrEmpty(p.Arguments) {
				result.Result = toolContent("Esta herramienta no admite argumentos ni cambios de alcance.", true)
			} else {
				call, cancel := context.WithTimeout(ctx, readTimeout)
				report, err := reader.Summary(call)
				expired := call.Err()
				cancel()
				if err != nil || expired != nil {
					result.Result = toolContent("No se pudo leer el resumen.", true)
					break
				}
				text, err := json.Marshal(report)
				if err != nil {
					return err
				}
				result.Result = toolContent(string(text), false)
			}
		default:
			result = errorResponse(r.ID, -32601, "Method not found")
		}
		if err := encoder.Encode(result); err != nil {
			return err
		}
	}
	return scan.Err()
}

func toolContent(text string, failed bool) any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": failed}
}
