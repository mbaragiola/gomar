package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMCPProposalCLIPrerequisites(t *testing.T) {
	const child = "GOMAR_MCP_STARTUP_TEST_CHILD"
	if os.Getenv(child) == "1" {
		os.Args = append(os.Args[:1], flag.Args()...)
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		main()
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, want string, switches ...string) {
		t.Helper()
		args := append([]string{"-test.run=^TestMCPProposalCLIPrerequisites$", "--", "-state", t.TempDir()}, switches...)
		cmd := exec.CommandContext(t.Context(), self, args...)
		cmd.Env = []string{child + "=1"}
		out, err := cmd.CombinedOutput()
		if code, ok := err.(*exec.ExitError); !ok || code.ExitCode() != 1 ||
			!strings.Contains(string(out), want) || strings.Contains(string(out), "http://") {
			t.Fatalf("CLI should stop before serving with %q, got %v: %s", want, err, out)
		}
	}
	for _, proposal := range []string{
		"wiki-proposals", "document-proposals", "wiki-edit-proposals", "stock-receipt-proposals", "proposal-sessions",
		"supplier-legal-name-proposals", "stock-product-setup-proposals", "stock-tracking-proposals", "stock-minimum-proposals",
		"pos-create-proposals", "reservation-cancel-proposals", "outlet-display-label-proposals",
		"reservation-policy-notice-proposals", "reservation-slot-add-proposals", "reservation-table-add-proposals",
		"reservation-confirm-proposals", "reconciliation-draft-proposals", "pos-catalog-proposals", "stock-adjustment-proposals",
		"document-point-of-sale-proposals", "document-number-proposals", "document-edit-proposals", "document-review-proposals",
	} {
		t.Run(proposal, func(t *testing.T) {
			switchName := "-enable-mcp-" + proposal
			check(t, switchName+" requires -enable-mcp", switchName)
		})
	}
	t.Run("MCP explicitly disabled", func(t *testing.T) {
		check(t, "-enable-mcp-wiki-proposals requires -enable-mcp", "-enable-mcp=false", "-enable-mcp-wiki-proposals")
	})
	t.Run("MCP without proposals", func(t *testing.T) {
		check(t, "PostgreSQL connection settings are missing", "-enable-mcp", "-enable-mcp-wiki-proposals=false")
	})
	t.Run("feature prerequisites remain", func(t *testing.T) {
		check(t, "-enable-mcp-pos-catalog-proposals requires -enable-pos", "-enable-mcp", "-enable-mcp-pos-catalog-proposals")
		check(t, "-enable-mcp-stock-minimum-proposals requires -enable-pos and -enable-stock",
			"-enable-mcp", "-enable-pos", "-enable-mcp-stock-minimum-proposals")
		check(t, "-enable-mcp-reservation-confirm-proposals requires -public-reservations",
			"-enable-mcp", "-enable-mcp-reservation-confirm-proposals")
	})
	t.Run("valid startup reaches the database", func(t *testing.T) {
		check(t, "PostgreSQL connection settings are missing", "-enable-mcp", "-enable-mcp-wiki-proposals",
			"-enable-mcp-reservation-table-add-proposals", "-enable-pos", "-enable-stock",
			"-enable-mcp-stock-minimum-proposals")
	})
}
