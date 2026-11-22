package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	claudePath       = "/mcp"
	maxHTTPBody      = 16 * 1024
	maxHTTPRequests  = 256
	maxHTTPInFlight  = 8
	claudeSessionTTL = 30 * time.Second
)

// claudeHandler protects a synthetic-only, same-machine HTTP preview. It does
// not authenticate local clients and must not be used for any real data.
func claudeHandler(host string, next http.Handler) http.Handler {
	var accepted atomic.Uint32
	active := make(chan struct{}, maxHTTPInFlight)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != host || r.URL.IsAbs() {
			http.Error(w, "Invalid local host", http.StatusForbidden)
			return
		}
		for _, header := range []string{"Origin", "Authorization", "Proxy-Authorization", "Cookie"} {
			// The presence of an empty header is still outside the local contract.
			if _, present := r.Header[header]; present {
				http.Error(w, "Origin and credentials are not accepted", http.StatusForbidden)
				return
			}
		}
		if r.URL.Path != claudePath || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodPost, http.MethodDelete:
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength > maxHTTPBody {
			http.Error(w, "Request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		select {
		case active <- struct{}{}:
			defer func() { <-active }()
		default:
			http.Error(w, "Preview is busy", http.StatusServiceUnavailable)
			return
		}
		for {
			n := accepted.Load()
			if n >= maxHTTPRequests {
				http.Error(w, "Preview request limit reached; restart the demo", http.StatusServiceUnavailable)
				return
			}
			if accepted.CompareAndSwap(n, n+1) {
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ServeClaude starts a synthetic preview on an ephemeral IPv4 loopback port.
// The only stdout output is the URL; the owner stops the process to remove it.
func ServeClaude(ctx context.Context, out io.Writer) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("open local preview: %w", err)
	}
	defer ln.Close()

	s := server()
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		SessionTimeout:               claudeSessionTTL,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          maxHTTPBody,
		PropagateRequestCancellation: true,
	})
	host := ln.Addr().String()
	h := &http.Server{
		Handler:           claudeHandler(host, transport),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    8 * 1024,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	if _, err := fmt.Fprintf(out, "http://%s%s\n", host, claudePath); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		wait, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownErr := h.Shutdown(wait)
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, h.Close())
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return errors.Join(shutdownErr, err)
		}
		return shutdownErr
	}
}
