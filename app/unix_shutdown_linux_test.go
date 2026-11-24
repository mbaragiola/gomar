package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProtectedUnixShutdownChild(t *testing.T) {
	path := os.Getenv("GOMAR_UNIX_SHUTDOWN_TEST_SOCKET")
	if path == "" {
		return
	}
	grace, err := time.ParseDuration(os.Getenv("GOMAR_UNIX_SHUTDOWN_TEST_GRACE"))
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := listenProtectedUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		DisableGeneralOptionsHandler: true,
		BaseContext:                  ingress.baseContext,
		ConnContext:                  ingress.connContext,
		Handler: ingress.wrapHTTPS(syntheticAuthority, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(os.Stdout, "active")
			if _, err := io.Copy(io.Discard, os.Stdin); err != nil || r.Context().Err() != nil {
				http.Error(w, "interrupted", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})),
	}
	if err := ingress.serve(server, grace, func() { fmt.Fprintln(os.Stdout, "ready") }); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedUnixShutdownLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sig    os.Signal
		forced bool
	}{
		{"SIGTERM drains", syscall.SIGTERM, false},
		{"SIGINT drains", os.Interrupt, false},
		{"deadline force closes", syscall.SIGTERM, true},
	} {
		t.Run(tc.name, func(t *testing.T) { checkProtectedUnixShutdown(t, tc.sig, tc.forced) })
	}
}

func checkProtectedUnixShutdown(t *testing.T, sig os.Signal, forced bool) {
	t.Helper()
	path := filepath.Join(localUnixTestDirectory(t), "socket")
	grace := 5 * time.Second
	if forced {
		grace = 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestProtectedUnixShutdownChild$", "-test.timeout=10s")
	cmd.Env = []string{"GOMAR_UNIX_SHUTDOWN_TEST_SOCKET=" + path, "GOMAR_UNIX_SHUTDOWN_TEST_GRACE=" + grace.String()}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	markers := bufio.NewReader(out)
	wantMarker := func(want string) {
		t.Helper()
		if got, err := markers.ReadString('\n'); err != nil || got != want+"\n" {
			more, _ := io.ReadAll(markers)
			t.Fatalf("subprocess marker = %q (%v), want %q; remaining output: %s", got, err, want, more)
		}
	}
	wantMarker("ready")
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sendShutdownFixtureRequest(t, conn)
	wantMarker("active")
	started := time.Now()
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	deadline := started.Add(3 * time.Second)
	for {
		extra, err := net.DialTimeout("unix", path, 50*time.Millisecond)
		if errors.Is(err, syscall.ECONNREFUSED) {
			break
		}
		if err == nil {
			extra.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener still accepting after %s (last dial: %v)", sig, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !forced {
		if err := in.Close(); err != nil {
			t.Fatal(err)
		}
	}
	checkShutdownFixtureResponse(t, conn, forced)
	if forced && time.Since(started) > 4*time.Second {
		t.Fatal("stalled connection exceeded its drain limit")
	}
	transcript, err := io.ReadAll(markers)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); forced {
		var failed *exec.ExitError
		if !errors.As(err, &failed) || failed.ExitCode() != 1 ||
			!strings.Contains(string(transcript), "drain protected Unix HTTP requests: context deadline exceeded") {
			t.Fatalf("expected a reported drain timeout, got %v: %s", err, transcript)
		}
	} else if err != nil {
		t.Fatalf("expected a clean shutdown, got %v: %s", err, transcript)
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode() != os.ModeSocket|0600 || !os.SameFile(before, after) {
		t.Fatalf("shutdown changed the owner-only socket node: %v", err)
	}
	if stale, err := listenProtectedUnix(path); err == nil {
		stale.listener.Close()
		t.Fatal("startup reused an occupied socket path")
	}
	fresh, err := listenProtectedUnix(filepath.Join(localUnixTestDirectory(t), "socket"))
	if err != nil {
		t.Fatalf("restart in a fresh private directory: %v", err)
	}
	fresh.listener.Close()
}

func sendShutdownFixtureRequest(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "GET /wait HTTP/1.1\r\nHost: %s\r\nX-Forwarded-Proto: https\r\nConnection: close\r\n\r\n", syntheticAuthority); err != nil {
		t.Fatal(err)
	}
}

func checkShutdownFixtureResponse(t *testing.T, conn net.Conn, forced bool) {
	t.Helper()
	if forced {
		var first [1]byte
		if n, err := conn.Read(first[:]); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("stalled connection not closed: read %d bytes, %v", n, err)
		}
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("active request did not complete: %d", response.StatusCode)
	}
}

// The in-memory listener exercises the same drain without AF_UNIX access.
type shutdownPipeListener struct {
	conn   net.Conn
	closed chan struct{}
}

func (l *shutdownPipeListener) Accept() (net.Conn, error) {
	if l.conn != nil {
		conn := l.conn
		l.conn = nil
		return conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *shutdownPipeListener) Close() error {
	close(l.closed) // http.Server closes its listener once.
	return nil
}

func (*shutdownPipeListener) Addr() net.Addr { return nil }

func TestHTTPShutdownDrain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grace  time.Duration
		forced bool
	}{
		{"completes active request", 3 * time.Second, false},
		{"force-closes stalled request", 50 * time.Millisecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverConn, clientConn := net.Pipe()
			defer serverConn.Close()
			defer clientConn.Close()
			l := &shutdownPipeListener{conn: serverConn, closed: make(chan struct{})}
			active, release := make(chan struct{}), make(chan struct{})
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(active)
				select {
				case <-release:
					w.WriteHeader(http.StatusNoContent)
				case <-r.Context().Done():
				}
			})}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			stop, signalStop := context.WithCancel(ctx)
			defer signalStop()
			done := make(chan error, 1)
			go func() { done <- serveHTTPUntilStop(stop, server, l, tc.grace) }()
			wait := func(ch <-chan struct{}, operation string) {
				t.Helper()
				select {
				case <-ch:
				case <-ctx.Done():
					t.Fatalf("timed out waiting for %s", operation)
				}
			}
			sendShutdownFixtureRequest(t, clientConn)
			wait(active, "the active handler")
			signalStop()
			wait(l.closed, "the listener to close")
			if !tc.forced {
				close(release)
			}
			checkShutdownFixtureResponse(t, clientConn, tc.forced)
			select {
			case err := <-done:
				if tc.forced && !errors.Is(err, context.DeadlineExceeded) || !tc.forced && err != nil {
					t.Fatalf("drain result: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("drain did not return")
			}
		})
	}
}
