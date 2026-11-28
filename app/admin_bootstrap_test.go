package main

import (
	"strings"
	"testing"
)

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
