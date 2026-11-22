package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const microsoftConsumerTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

var providerTenantID = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
var errProviderConfig = errors.New("invalid identity provider configuration")
var errProviderIdentity = errors.New("invalid identity provider response")

type providerClientSettings struct {
	ClientID string
	// For Apple, pass a current Services ID client secret JWT obtained
	// securely from the operator, not a signing key or ordinary password.
	ClientSecret string
	Tenant       string
}

type providerEndpoints struct {
	Name, Issuer, Auth, Token, JWKS, Tenant string
	FormPost, PKCE                          bool
}

func pinnedProviderEndpoints(flow, tenant string) (providerEndpoints, error) {
	switch flow {
	case "google":
		if tenant != "" {
			return providerEndpoints{}, errProviderConfig
		}
		return providerEndpoints{
			Name: "google", Issuer: "https://accounts.google.com", Auth: "https://accounts.google.com/o/oauth2/v2/auth",
			Token: "https://oauth2.googleapis.com/token", JWKS: "https://www.googleapis.com/oauth2/v3/certs", PKCE: true,
		}, nil
	case "apple":
		if tenant != "" {
			return providerEndpoints{}, errProviderConfig
		}
		return providerEndpoints{
			Name: "apple", Issuer: "https://appleid.apple.com", Auth: "https://appleid.apple.com/auth/authorize",
			Token: "https://appleid.apple.com/auth/token", JWKS: "https://appleid.apple.com/auth/keys", FormPost: true,
		}, nil
	case "microsoft", "microsoft-work":
		const host = "https://login.microsoftonline.com"
		authority := tenant
		if flow == "microsoft" {
			if tenant != "" {
				return providerEndpoints{}, errProviderConfig
			}
			authority, tenant = "consumers", microsoftConsumerTenant
		} else if !providerTenantID.MatchString(tenant) || tenant == microsoftConsumerTenant || tenant == "00000000-0000-0000-0000-000000000000" {
			return providerEndpoints{}, errProviderConfig
		}
		return providerEndpoints{
			Name: "microsoft", Issuer: host + "/" + tenant + "/v2.0",
			Auth: host + "/" + authority + "/oauth2/v2.0/authorize", Token: host + "/" + authority + "/oauth2/v2.0/token",
			JWKS: host + "/" + authority + "/discovery/v2.0/keys", Tenant: tenant, PKCE: true,
		}, nil
	default:
		return providerEndpoints{}, errProviderConfig
	}
}

type providerOIDC struct {
	endpoints providerEndpoints
	config    oauth2.Config
	http      *http.Client
	verifier  *oidc.IDTokenVerifier
}

type providerOutbound struct {
	token, jwks, microsoftIssuer string
	base                         http.RoundTripper
}

func (t providerOutbound) RoundTrip(r *http.Request) (*http.Response, error) {
	if (r.Method != http.MethodPost || r.URL.String() != t.token) && (r.Method != http.MethodGet || r.URL.String() != t.jwks) {
		return nil, errProviderConfig
	}
	out, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, errProviderIdentity
	}
	defer out.Body.Close()
	const max = 1 << 20
	data, err := io.ReadAll(io.LimitReader(out.Body, max+1))
	if err != nil || len(data) > max {
		return nil, errProviderIdentity
	}
	if t.microsoftIssuer != "" && r.URL.String() == t.jwks && out.StatusCode == http.StatusOK {
		// Microsoft keys can themselves be scoped to a tenant. Check that
		// constraint before handing any keys to the OIDC verifier.
		var set struct {
			Keys []json.RawMessage `json:"keys"`
		}
		if json.Unmarshal(data, &set) != nil {
			return nil, errProviderIdentity
		}
		var eligible = make([]json.RawMessage, 0, len(set.Keys))
		for _, raw := range set.Keys {
			var key struct {
				Issuer string `json:"issuer"`
			}
			if json.Unmarshal(raw, &key) == nil &&
				(key.Issuer == t.microsoftIssuer || key.Issuer == "https://login.microsoftonline.com/{tenantid}/v2.0") {
				eligible = append(eligible, raw)
			}
		}
		if len(eligible) == 0 {
			return nil, errProviderIdentity
		}
		set.Keys = eligible
		data, _ = json.Marshal(set)
	}
	out.ContentLength = int64(len(data))
	out.Body = io.NopCloser(bytes.NewReader(data))
	return out, nil
}

func newOIDCProvider(origin, flow string, settings providerClientSettings, base http.RoundTripper) (*providerOIDC, error) {
	e, err := pinnedProviderEndpoints(flow, settings.Tenant)
	if err != nil {
		return nil, err
	}
	if !validProviderSetting(settings.ClientID, 255) || !validProviderSetting(settings.ClientSecret, 4096) {
		return nil, errProviderConfig
	}
	if base == nil {
		base = http.DefaultTransport
	}
	transport := providerOutbound{token: e.Token, jwks: e.JWKS, base: base}
	if e.Name == "microsoft" {
		transport.microsoftIssuer = e.Issuer
	}
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	oauth := oauth2.Config{
		ClientID: settings.ClientID, ClientSecret: settings.ClientSecret,
		RedirectURL: origin + "/api/v1/auth/providers/" + flow + "/callback",
		Scopes:      []string{oidc.ScopeOpenID},
		Endpoint:    oauth2.Endpoint{AuthURL: e.Auth, TokenURL: e.Token, AuthStyle: oauth2.AuthStyleInParams},
	}
	// Apple's REST authorization endpoint does not request the OIDC
	// "openid" scope; a code exchange returns the signed identity token.
	// Request no identity details beyond the required subject.
	if e.FormPost {
		oauth.Scopes = nil
	}
	keySet := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), e.JWKS)
	verifier := oidc.NewVerifier(e.Issuer, keySet, &oidc.Config{ClientID: settings.ClientID, SupportedSigningAlgs: []string{oidc.RS256}})
	return &providerOIDC{e, oauth, client, verifier}, nil
}

func validProviderSetting(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 33 || value[i] > 126 {
			return false
		}
	}
	return true
}

func (p *providerOIDC) authorizationURL(state, nonce, verifier string) string {
	options := []oauth2.AuthCodeOption{oidc.Nonce(nonce)}
	if p.endpoints.PKCE {
		options = append(options, oauth2.S256ChallengeOption(verifier))
	}
	if p.endpoints.FormPost {
		options = append(options, oauth2.SetAuthURLParam("response_mode", "form_post"))
	}
	return p.config.AuthCodeURL(state, options...)
}

type verifiedProviderIdentity struct{ Provider, Issuer, Subject string }

func (p *providerOIDC) exchange(ctx context.Context, code, verifier string, wantNonce []byte) (verifiedProviderIdentity, error) {
	var empty verifiedProviderIdentity
	ctx = oidc.ClientContext(ctx, p.http)
	var options []oauth2.AuthCodeOption
	if p.endpoints.PKCE {
		options = append(options, oauth2.VerifierOption(verifier))
	}
	result, err := p.config.Exchange(ctx, code, options...)
	if err != nil {
		return empty, errProviderIdentity
	}
	raw, ok := result.Extra("id_token").(string)
	if !ok || len(raw) == 0 || len(raw) > 16<<10 {
		return empty, errProviderIdentity
	}
	id, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return empty, errProviderIdentity
	}
	var claims struct {
		Tenant          string `json:"tid"`
		AuthorizedParty string `json:"azp"`
	}
	now := time.Now()
	nonce := sha256.Sum256([]byte(id.Nonce))
	if id.Issuer != p.endpoints.Issuer || len(id.Audience) != 1 || id.Audience[0] != p.config.ClientID ||
		id.IssuedAt.Before(now.Add(-15*time.Minute)) || id.IssuedAt.After(now.Add(2*time.Minute)) ||
		len(id.Subject) == 0 || len(id.Subject) > 255 || !utf8.ValidString(id.Subject) || strings.ContainsRune(id.Subject, 0) ||
		id.Nonce == "" || subtle.ConstantTimeCompare(wantNonce, nonce[:]) != 1 || id.Claims(&claims) != nil ||
		(claims.AuthorizedParty != "" && claims.AuthorizedParty != p.config.ClientID) ||
		(p.endpoints.Tenant != "" && claims.Tenant != p.endpoints.Tenant) {
		return empty, errProviderIdentity
	}
	return verifiedProviderIdentity{p.endpoints.Name, id.Issuer, id.Subject}, nil
}

type hostedProviderAuth struct {
	origin    string
	host      string
	providers map[string]*providerOIDC
}

func newHostedProviderAuth(origin string, settings map[string]providerClientSettings, network http.RoundTripper) (*hostedProviderAuth, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.String() != origin {
		return nil, errProviderConfig
	}
	// Hosted auth must be bound to a configured authority, not Host or
	// X-Forwarded-Host data read from an incoming request.
	if !validUnixAuthority(u.Host) || len(settings) == 0 {
		return nil, errProviderConfig
	}
	out := &hostedProviderAuth{origin: origin, host: u.Host, providers: make(map[string]*providerOIDC, len(settings))}
	for flow, v := range settings {
		p, err := newOIDCProvider(origin, flow, v, network)
		if err != nil {
			return nil, err
		}
		out.providers[flow] = p
	}
	return out, nil
}
