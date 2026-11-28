package main

import (
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAdminBootstrapCLI(t *testing.T) {
	const child = "GOMAR_ADMIN_BOOTSTRAP_TEST_CHILD"
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
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"empty username", "provide a local administrator username", []string{"-create-admin", ""}},
		{"blank username", "provide a local administrator username", []string{"-create-admin", " \t"}},
		{"omitted flag", "PostgreSQL connection settings are missing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-test.run=^TestAdminBootstrapCLI$", "--", "-state", t.TempDir()}, tc.args...)
			cmd := exec.CommandContext(t.Context(), self, args...)
			cmd.Env = []string{child + "=1"}
			output, err := cmd.CombinedOutput()
			if code, ok := err.(*exec.ExitError); !ok || code.ExitCode() != 1 ||
				!strings.Contains(string(output), tc.want) || strings.Contains(string(output), "http://") {
				t.Fatalf("expected CLI to reject before serving with %q, got %v: %s", tc.want, err, output)
			}
		})
	}
}

func TestReadLocalAdminPassword(t *testing.T) {
	for _, tt := range []struct {
		name, line, wantError string
	}{
		{"required", "\n", requiredPassword},
		{"null", "long\x00validation\n", nullPassword},
		{"invalid UTF-8", "long\xffvalidation\n", "texto no válido"},
		{"short multibyte", "ñéôåÜø茶\n", shortPassword},
		{"common", "password\n", commonPassword},
		{"numeric", "١٢٣٤٥٦٧٨\n", numericPassword},
		{"similar to username", "local-admin!\n", similarPassword},
		{"missing newline", "fixture phrase", "including its newline"},
		{"over limit", strings.Repeat("é", 2048) + "\n", "at most 4096 bytes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			password, err := readLocalAdminPassword(strings.NewReader(tt.line), "local-admin")
			if password != "" || err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected policy rejection %q, got %v", tt.wantError, err)
			}
		})
	}

	const spaced = "  níquel zorro eclipse  "
	for _, terminator := range []string{"\n", "\r\n"} {
		password, err := readLocalAdminPassword(strings.NewReader(spaced+terminator), "local-admin")
		if password != spaced || err != nil {
			t.Fatalf("readLocalAdminPassword did not preserve the password: %v", err)
		}
	}
	longPassword := strings.Repeat("z", 4095)
	password, err := readLocalAdminPassword(strings.NewReader(longPassword+"\n"), "local-admin")
	if password != longPassword || err != nil {
		t.Fatalf("readLocalAdminPassword rejected a line at the input limit: %v", err)
	}
}
