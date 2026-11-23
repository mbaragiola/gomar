package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixtureLaneToken(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func putLaneFixture(t *testing.T, dir, name string, fill byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(fixtureLaneToken(fill)), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func laneArgs(origin, read, rename, minimum string) []string {
	return []string{"--origin=" + origin, "--token-file=" + read, "--proposal-entity=7", "--proposal-outlet=8",
		"--proposal-lane=pos_product_rename=" + rename, "--proposal-lane=stock_minimum_change=" + minimum}
}

func TestProposalLaneInvalidConfigHasNoHTTPOrSensitiveError(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	read, rename, min := putLaneFixture(t, real, "read", 1), putLaneFixture(t, real, "rename", 2), putLaneFixture(t, real, "minimum", 3)
	copied := putLaneFixture(t, real, "copy_of_rename", 2)
	insecure := putLaneFixture(t, real, "readable_file", 4)
	if err := os.Chmod(insecure, 0640); err != nil {
		t.Fatal(err)
	}
	link, hardlink, dirlink := filepath.Join(real, "link"), filepath.Join(real, "hard"), filepath.Join(real, "dirlink")
	if err := os.Symlink(min, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(rename, hardlink); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(real, "real_parent")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	putLaneFixture(t, sub, "minimum", 3)
	if err := os.Symlink(sub, dirlink); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(http.StatusTeapot) }))
	defer web.Close()
	makeArgs := func(one, two string) []string { return laneArgs(web.URL, read, one, two) }
	valid := makeArgs(rename, min)
	tests := [][]string{
		valid[:len(valid)-1],
		append(append([]string{}, valid...), "--proposal-entity=7"),
		append(append([]string{}, valid...), "--proposal-session-action=pos_product_rename"),
		append(append([]string{}, valid...), "--wiki-proposal-token-file="),
		append(append([]string{}, valid[:len(valid)-1]...), "--proposal-lane=unknown="+min),
		append(append([]string{}, valid[:len(valid)-1]...), "--proposal-lane=pos_product_rename="+min),
		makeArgs(rename, read), makeArgs(rename, rename), makeArgs(rename, hardlink), makeArgs(rename, copied), makeArgs(rename, insecure),
		makeArgs(rename, link), makeArgs(rename, filepath.Join(dirlink, "minimum")), makeArgs(rename, "missing/relative"),
	}
	for i, args := range tests {
		var output bytes.Buffer
		err := runLive(t.Context(), args, strings.NewReader(""), &output)
		if err == nil || output.Len() != 0 || strings.Contains(err.Error(), real) || strings.Contains(err.Error(), fixtureLaneToken(2)) {
			t.Fatalf("invalid case %d was not silent and fail-closed", i)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid configuration reached HTTP")
	}
}

func TestTwoProposalLanesDoNotShareCredentialsOrFallback(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	read, rename, min := putLaneFixture(t, dir, "read", 1), putLaneFixture(t, dir, "rename", 2), putLaneFixture(t, dir, "min", 3)
	var renameDenied atomic.Bool
	var reads, renames, minimums atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		bearer := r.Header.Get("Authorization")
		const base = "/api/v1/mcp/entities/7/outlets/8"
		switch r.URL.Path {
		case "/api/v1/mcp/me":
			reads.Add(1)
			if bearer != "Bearer "+fixtureLaneToken(1) || r.Method != "GET" {
				t.Error("read used wrong credential or verb")
			}
			_, _ = io.WriteString(w, `{"id":7,"username":"fixture"}`)
		case base + "/pos-rename-proposals":
			renames.Add(1)
			if bearer != "Bearer "+fixtureLaneToken(2) || r.Method != "POST" {
				t.Error("rename used wrong credential or verb")
			}
			if renameDenied.Load() {
				w.WriteHeader(401)
				_, _ = io.WriteString(w, `{"detail":"private-backend-marker"}`)
				return
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":1,"status":"pending","expires_at":"2030-12-01T03:00:01Z"}`)
		case base + "/stock-minimum-proposals":
			minimums.Add(1)
			if bearer != "Bearer "+fixtureLaneToken(3) || r.Method != "POST" {
				t.Error("minimum used wrong credential or verb")
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":2,"status":"pending","expires_at":"2030-12-01T03:00:01Z"}`)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer web.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	up, send := io.Pipe()
	down, recv := io.Pipe()
	defer up.Close()
	defer send.Close()
	defer down.Close()
	defer recv.Close()
	go func() { defer recv.Close(); _ = runLive(ctx, laneArgs(web.URL, read, rename, min), up, recv) }()
	peer := mcp.NewClient(&mcp.Implementation{Name: "synthetic-lane-qa", Version: "1"}, nil)
	session, err := peer.Connect(ctx, &mcp.IOTransport{Reader: down, Writer: send}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 39 {
		t.Fatal("expected 37 read tools and two distinct proposal tools", err)
	}
	for _, tool := range list.Tools {
		if !tool.Annotations.ReadOnlyHint && tool.Name != "gomar_propose_pos_rename" && tool.Name != "gomar_propose_stock_minimum_change" {
			t.Fatal("unexpected proposal registrar")
		}
	}
	check := func(name string, entity, outlet int64, wantErr bool) {
		t.Helper()
		args := map[string]any{}
		if name != "gomar_current_user" {
			args = map[string]any{"entity_id": entity, "outlet_id": outlet, "request_id": "abababab-abab-4aba-8aba-abababababab"}
			if name == "gomar_propose_pos_rename" {
				args["rename"] = map[string]any{"product_id": 1, "expected_revision": 2, "name": "Synthetic rename"}
			} else {
				args["minimum_change"] = map[string]any{"product_id": 1, "expected_revision": 2, "minimum": 0}
			}
		}
		ret, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || ret.IsError != wantErr {
			t.Fatalf("unexpected result for %s (error=%v)", name, err)
		}
		raw, _ := json.Marshal(ret)
		for _, secret := range []string{dir, fixtureLaneToken(1), fixtureLaneToken(2), fixtureLaneToken(3), "private-backend-marker"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("unexpected private payload")
			}
		}
	}
	check("gomar_current_user", 7, 8, false)
	check("gomar_propose_pos_rename", 7, 8, false)
	check("gomar_propose_stock_minimum_change", 7, 8, false)
	check("gomar_propose_pos_rename", 7, 9, true)
	check("gomar_propose_stock_minimum_change", 6, 8, true)
	renameDenied.Store(true)
	check("gomar_propose_pos_rename", 7, 8, true)
	check("gomar_propose_stock_minimum_change", 7, 8, false)
	check("gomar_current_user", 7, 8, false)
	if reads.Load() != 2 || renames.Load() != 2 || minimums.Load() != 2 {
		t.Fatal("a denied call reached another lane or escaped its scope")
	}
}
