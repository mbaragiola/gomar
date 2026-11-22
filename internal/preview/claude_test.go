package preview

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testHost = "127.0.0.1:43129"

func claudeRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Host = testHost
	return r
}

func TestClaudeLoopbackGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		change func(*http.Request)
		want   int
	}{
		{"post", "POST", "/mcp", nil, http.StatusNoContent},
		{"stream", "GET", "/mcp", nil, http.StatusNoContent},
		{"delete session", "DELETE", "/mcp", nil, http.StatusNoContent},
		{"different host", "POST", "/mcp", func(r *http.Request) { r.Host = "localhost:43129" }, http.StatusForbidden},
		{"absolute target", "POST", "http://127.0.0.1:43129/mcp", nil, http.StatusForbidden},
		{"origin present", "POST", "/mcp", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:43129") }, http.StatusForbidden},
		{"empty origin", "POST", "/mcp", func(r *http.Request) { r.Header.Set("Origin", "") }, http.StatusForbidden},
		{"authorization", "POST", "/mcp", func(r *http.Request) { r.Header.Set("Authorization", "example") }, http.StatusForbidden},
		{"proxy authorization", "POST", "/mcp", func(r *http.Request) { r.Header.Set("Proxy-Authorization", "example") }, http.StatusForbidden},
		{"cookie", "POST", "/mcp", func(r *http.Request) { r.Header.Set("Cookie", "example=1") }, http.StatusForbidden},
		{"other path", "POST", "/status", nil, http.StatusNotFound},
		{"nested path", "POST", "/mcp/", nil, http.StatusNotFound},
		{"encoded alias", "POST", "/%6dcp", nil, http.StatusNotFound},
		{"query", "POST", "/mcp?x=1", nil, http.StatusNotFound},
		{"empty query", "POST", "/mcp?", nil, http.StatusNotFound},
		{"other verb", "PUT", "/mcp", nil, http.StatusMethodNotAllowed},
		{"oversize", "POST", "/mcp", func(r *http.Request) { r.ContentLength = maxHTTPBody + 1 }, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler := claudeHandler(testHost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			req := claudeRequest(tc.method, tc.path)
			if tc.change != nil {
				tc.change(req)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want || (calls == 1) != (tc.want == http.StatusNoContent) ||
				response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("HTTP %d, handler calls %d; want HTTP %d", response.Code, calls, tc.want)
			}
		})
	}
}

func TestClaudeRequestBudget(t *testing.T) {
	handler := claudeHandler(testHost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i <= maxHTTPRequests; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, claudeRequest("POST", "/mcp"))
		want := http.StatusNoContent
		if i == maxHTTPRequests {
			want = http.StatusServiceUnavailable
		}
		if w.Code != want {
			t.Fatalf("request %d gave %d, want %d", i+1, w.Code, want)
		}
	}
}
