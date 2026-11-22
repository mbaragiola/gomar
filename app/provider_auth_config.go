package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const providerAuthMigration = "015_provider_auth.sql"
const providerAppleMaxSecretAge = 15777000 * time.Second

var errProviderNeedsSchema = errors.New("provider login requires its registered and applied PostgreSQL migration")
var errProviderNeedsHTTPS = errors.New("provider login requires protected HTTPS Unix ingress on the exact configured public origin")
var errProviderSecretFile = errors.New("provider secret must be in a private service-owned regular file")
var appleIdentifier = regexp.MustCompile("^[A-Z0-9]{10}$")

type providerRuntimeSettings struct {
	enabled          bool
	protectedIngress bool
	authority        string
	migrations       []string
	env              func(string) string
	readSecret       func(string) (string, error)
	network          http.RoundTripper
}

func providerRuntimeAuth(cfg providerRuntimeSettings) (*hostedProviderAuth, error) {
	if !cfg.enabled {
		return nil, nil
	}
	if !cfg.protectedIngress || !validUnixAuthority(cfg.authority) || cfg.env("GOMAR_AUTH_PUBLIC_ORIGIN") != "https://"+cfg.authority {
		return nil, errProviderNeedsHTTPS
	}
	// Do not even open a configured secret until this build knows the
	// schema. Normal startup separately verifies that it is applied.
	if !slices.Contains(cfg.migrations, providerAuthMigration) {
		return nil, errProviderNeedsSchema
	}
	settings, err := readProviderRuntimeSettings(cfg.env, cfg.readSecret)
	if err != nil {
		return nil, err
	}
	return newHostedProviderAuth("https://"+cfg.authority, settings, cfg.network)
}

func readProviderRuntimeSettings(env func(string) string, readSecret func(string) (string, error)) (map[string]providerClientSettings, error) {
	selected := strings.Split(env("GOMAR_AUTH_PROVIDERS"), ",")
	if len(selected) > 3 || slices.Contains(selected, "apple") {
		return nil, errProviderConfig
	}
	result := make(map[string]providerClientSettings, len(selected))
	for _, flow := range selected {
		var prefix string
		switch flow {
		case "google":
			prefix = "GOMAR_AUTH_GOOGLE"
		case "microsoft":
			prefix = "GOMAR_AUTH_MICROSOFT"
		case "microsoft-work":
			prefix = "GOMAR_AUTH_MICROSOFT_WORK"
		default:
			return nil, errProviderConfig
		}
		if _, exists := result[flow]; exists {
			return nil, errProviderConfig
		}
		v := providerClientSettings{ClientID: env(prefix + "_CLIENT_ID")}
		if flow == "microsoft-work" {
			v.Tenant = env(prefix + "_TENANT_ID")
		}
		if !validProviderSetting(v.ClientID, 255) {
			return nil, errProviderConfig
		}
		if _, err := pinnedProviderEndpoints(flow, v.Tenant); err != nil {
			return nil, err
		}
		secret, err := readSecret(env(prefix + "_CLIENT_SECRET_FILE"))
		if err != nil {
			return nil, errProviderSecretFile
		}
		v.ClientSecret = secret
		if !validProviderSetting(v.ClientSecret, 4096) {
			return nil, errProviderSecretFile
		}
		result[flow] = v
	}
	return result, nil
}

func readProviderSecretFile(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errProviderSecretFile
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", errProviderSecretFile
	}
	file := os.NewFile(uintptr(fd), "provider secret")
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || (st.Mode().Perm() != 0400 && st.Mode().Perm() != 0600) {
		return "", errProviderSecretFile
	}
	native, ok := st.Sys().(*syscall.Stat_t)
	if !ok || native.Uid != uint32(os.Geteuid()) {
		return "", errProviderSecretFile
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4099))
	if err != nil || len(raw) > 4098 {
		return "", errProviderSecretFile
	}
	secret := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if !validProviderSetting(secret, 4096) {
		return "", errProviderSecretFile
	}
	return secret, nil
}

// Startup can check shape, scope and clock without contacting Apple. This
// check cannot establish whether Apple accepts the secret's signature.
func validAppleClientSecretShape(raw, clientID string, now time.Time) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	decoder := base64.RawURLEncoding.Strict()
	h, err := decoder.DecodeString(parts[0])
	if err != nil {
		return false
	}
	b, err := decoder.DecodeString(parts[1])
	if err != nil {
		return false
	}
	sig, err := decoder.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	var payload struct {
		Issuer   string `json:"iss"`
		Subject  string `json:"sub"`
		Audience string `json:"aud"`
		Issued   int64  `json:"iat"`
		Expiry   int64  `json:"exp"`
	}
	if json.Unmarshal(h, &header) != nil || json.Unmarshal(b, &payload) != nil || header.Alg != "ES256" ||
		!appleIdentifier.MatchString(header.Kid) || !appleIdentifier.MatchString(payload.Issuer) ||
		payload.Subject != clientID || payload.Audience != "https://appleid.apple.com" || payload.Issued <= 0 || payload.Expiry <= payload.Issued {
		return false
	}
	iat, exp := time.Unix(payload.Issued, 0), time.Unix(payload.Expiry, 0)
	return !iat.After(now.Add(2*time.Minute)) && exp.After(now.Add(providerFlowAge)) && exp.Sub(iat) <= providerAppleMaxSecretAge
}
