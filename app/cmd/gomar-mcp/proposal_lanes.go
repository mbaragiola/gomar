package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This mode exposes only the two independently authorized lanes tested
// together. Legacy configurations and all other action registrars stay alone.
type laneRegistrar func(*mcp.Server, *mcpclient.ProposalClient)

func proposalLaneRegistrar(action string) laneRegistrar {
	switch action {
	case "pos_product_rename":
		return addPOSRenameProposalTool
	case "stock_minimum_change":
		return addStockMinimumProposalTool
	default:
		return nil
	}
}

var invalidProposalLanes = errors.New("configurá dos acciones distintas, cada una con su propio archivo privado, para una sola empresa y local")

type proposalLaneFlag []string

func (f *proposalLaneFlag) String() string { return "" }
func (f *proposalLaneFlag) Set(raw string) error {
	if len(*f) >= 2 || len(raw) > 2048 {
		return invalidProposalLanes
	}
	*f = append(*f, raw)
	return nil
}

type proposalLane struct {
	attach laneRegistrar
	file   string
}

func parseProposalLanes(fs *flag.FlagSet, values proposalLaneFlag, readFile string, entity, outlet int64) ([]proposalLane, error) {
	var legacy, hasEntity, hasOutlet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "proposal-entity":
			hasEntity = true
		case "proposal-outlet":
			hasOutlet = true
		case "wiki-proposal-token-file", "document-proposal-token-file",
			"wiki-edit-proposal-token-file", "stock-receipt-proposal-token-file",
			"proposal-session-token-file", "proposal-session-action":
			legacy = true
		}
	})
	if len(values) == 0 && !hasEntity && !hasOutlet {
		return nil, nil
	}
	if legacy || len(values) != 2 || entity <= 0 || outlet <= 0 || !hasEntity || !hasOutlet {
		return nil, invalidProposalLanes
	}
	seenActions := map[string]bool{}
	seenFiles := map[string]bool{filepath.Clean(readFile): true}
	result := make([]proposalLane, 0, len(values))
	for _, raw := range values {
		action, file, ok := strings.Cut(raw, "=")
		attach := proposalLaneRegistrar(action)
		if !ok || attach == nil || seenActions[action] || !filepath.IsAbs(file) ||
			filepath.Clean(file) != file || strings.ContainsAny(file, "\x00\r\n\t") || seenFiles[file] {
			return nil, invalidProposalLanes
		}
		seenActions[action], seenFiles[file] = true, true
		result = append(result, proposalLane{attach, file})
	}
	return result, nil
}

// Validate all references before reading any credential bytes in opt-in mode.
func validateProposalLaneFiles(readFile string, lanes []proposalLane) ([]os.FileInfo, error) {
	files := []string{readFile}
	for _, lane := range lanes {
		files = append(files, lane.file)
	}
	var prior []os.FileInfo
	for _, file := range files {
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil || resolved != file {
			return nil, invalidProposalLanes
		}
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, invalidProposalLanes
		}
		for _, previous := range prior {
			if os.SameFile(previous, info) {
				return nil, invalidProposalLanes
			}
		}
		prior = append(prior, info)
	}
	return prior, nil
}

func readBoundLaneToken(file string, pinned os.FileInfo) (string, error) {
	token, current, err := mcpclient.ReadTokenIdentity(file)
	if err != nil || !os.SameFile(pinned, current) {
		return "", invalidProposalLanes
	}
	return token, nil
}

func attachProposalLanes(server *mcp.Server, lanes []proposalLane, pins []os.FileInfo, origin string, entity, outlet int64, readToken string) error {
	used := map[string]bool{readToken: true}
	clients := make([]*mcpclient.ProposalClient, len(lanes))
	for i, lane := range lanes {
		token, err := readBoundLaneToken(lane.file, pins[i+1])
		if err != nil || used[token] {
			return invalidProposalLanes
		}
		used[token] = true
		clients[i], err = mcpclient.NewScopedProposalClient(origin, token, entity, outlet)
		if err != nil {
			return invalidProposalLanes
		}
	}
	for i, lane := range lanes {
		lane.attach(server, clients[i])
	}
	return nil
}

// flag.FlagSet keeps only the last occurrence. In the opt-in form refuse
// duplicate scope switches rather than silently redefining the branch.
func repeatedProposalLaneScope(args []string) bool {
	seen := map[string]bool{}
	for _, arg := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if strings.HasPrefix(arg, "-") && (name == "proposal-entity" || name == "proposal-outlet" || name == "origin" || name == "token-file") {
			if seen[name] {
				return true
			}
			seen[name] = true
		}
	}
	return false
}
