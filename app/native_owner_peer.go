package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	nativePeerSchema       = 1
	nativePeerRequestLimit = 8 << 10
	nativePeerReplyLimit   = 2 << 10
	nativePeerTimeout      = 2 * time.Second
)

// Owned by the app, not a browser, intent, or request. There is no runtime
// configuration or key source wired to this verifier. The test injects
// only ephemeral memory credentials and a private trust root.
type nativeFirstInstallPeerConfig struct {
	Endpoint, BillingName string
	Peer                  nativeInstallPeer
	Audience              string
	Roots                 *x509.CertPool
	BillingSPKI           [32]byte
	ClientIdentity        tls.Certificate
}

type nativePeerWireRequest struct {
	Schema             int      `json:"schema"`
	Issuer             string   `json:"issuer"`
	Recipient          string   `json:"recipient"`
	Purpose            string   `json:"purpose"`
	Audience           string   `json:"audience"`
	Intent             string   `json:"intent"`
	Business           string   `json:"business"`
	Initiator          string   `json:"initiator"`
	Claimant           string   `json:"claimant"`
	Branches           []string `json:"branches"`
	RequestRef         string   `json:"request_ref"`
	AcceptanceRef      string   `json:"acceptance_ref"`
	DesignationVersion int64    `json:"designation_version"`
	Nonce              string   `json:"nonce"`
}

type nativePeerWireReply struct {
	Schema    int    `json:"schema"`
	Issuer    string `json:"issuer"`
	Recipient string `json:"recipient"`
	Purpose   string `json:"purpose"`
	Digest    string `json:"binding_sha256"`
	Nonce     string `json:"nonce"`
	CheckedAt string `json:"checked_at"`
	ExpiresAt string `json:"expires_at"`
}

func nativePeerIdentifier(value string) bool {
	return len(value) <= 80 && nativeOwnerOpaqueID(value) && value[0] != '-' && value[0] != '_'
}

func nativePeerDNS(name string) bool {
	if len(name) > 253 || !strings.Contains(name, ".") || net.ParseIP(name) != nil {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func nativePeerEndpoint(value, name string) (*url.URL, bool) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery ||
		strings.Contains(value, "#") || u.RawPath != "" || u.EscapedPath() != u.Path || !strings.HasPrefix(u.Path, "/") ||
		u.Path == "/" || path.Clean(u.Path) != u.Path {
		return nil, false
	}
	for _, c := range u.Path {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '/' || c == '-' || c == '_') {
			return nil, false
		}
	}
	host := u.Hostname()
	// The loopback exception permits in-memory TLS tests with a real
	// configured Billing DNS identity and no resolver or network service.
	if host != name && !net.ParseIP(host).IsLoopback() {
		return nil, false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || u.Host != net.JoinHostPort(host, port) {
			return nil, false
		}
	} else if u.Host != host && u.Host != "["+host+"]" {
		return nil, false
	}
	return u, true
}

func nativePeerCanonicalRequest(cfg nativeFirstInstallPeerConfig, request nativeInstallRequest) (nativeInstallRequest, []byte, bool) {
	bad := func() (nativeInstallRequest, []byte, bool) { return nativeInstallRequest{}, nil, false }
	target, ok := canonicalNativeOwnerTarget(request.Target)
	nonce, nonceErr := base64.RawURLEncoding.Strict().DecodeString(request.Nonce)
	if !ok || request.Peer != cfg.Peer || request.Schema != nativePeerSchema || request.Purpose != nativeInstallPurpose ||
		target.Audience != cfg.Audience || !nativeOwnerRef(request.ConsentRef, "accept-") || !nativeOwnerRef(request.ApprovalRef, "approval-") ||
		request.Initiator == request.Claimant || nonceErr != nil || len(nonce) != 32 || len(request.Nonce) != 43 || bytes.Equal(nonce, make([]byte, 32)) {
		return bad()
	}
	for _, s := range []string{target.Intent, target.Business, target.RequestRef, request.Initiator, request.Claimant} {
		if !nativePeerIdentifier(s) {
			return bad()
		}
	}
	for _, b := range target.Outlets {
		if !nativePeerIdentifier(b) {
			return bad()
		}
	}
	wire := nativePeerWireRequest{
		Schema: nativePeerSchema, Issuer: cfg.Peer.Recipient, Recipient: cfg.Peer.Issuer, Purpose: nativeInstallPurpose,
		Audience: target.Audience, Intent: target.Intent, Business: target.Business, Initiator: request.Initiator, Claimant: request.Claimant,
		Branches: target.Outlets, RequestRef: target.RequestRef, AcceptanceRef: request.ConsentRef,
		DesignationVersion: target.Revision, Nonce: request.Nonce,
	}
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > nativePeerRequestLimit {
		return bad()
	}
	// ApprovalRef stays in the native binding. The private wire binds
	// the immutable acceptance ref, not a second approval parameter.
	request.Target = target
	return request, raw, true
}

func nativePeerDigest(canonical []byte) string {
	digest := sha256.Sum256(append([]byte("turnate/gomar/first-install/v1\x00"), canonical...))
	return hex.EncodeToString(digest[:])
}

func decodeNativePeerReply(data []byte) (nativePeerWireReply, bool) {
	var reply nativePeerWireReply
	if len(data) > nativePeerReplyLimit {
		return reply, false
	}
	fields := map[string]bool{"schema": true, "issuer": true, "recipient": true, "purpose": true,
		"binding_sha256": true, "nonce": true, "checked_at": true, "expires_at": true}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return reply, false
	}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !fields[key] {
			return reply, false
		}
		delete(fields, key)
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(raw, []byte("null")) {
			return reply, false
		}
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') || len(fields) != 0 {
		return reply, false
	}
	_, err = d.Token()
	if !errors.Is(err, io.EOF) || json.Unmarshal(data, &reply) != nil {
		return nativePeerWireReply{}, false
	}
	return reply, true
}

func nativePeerTime(value string) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, value)
	return at, err == nil && !at.IsZero() && at.UTC().Format(time.RFC3339Nano) == value
}

func nativePeerJSON(header http.Header) bool {
	mimeType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && len(header.Values("Content-Type")) == 1 && mimeType == "application/json" &&
		(len(params) == 0 || len(params) == 1 && strings.EqualFold(params["charset"], "utf-8"))
}

// newNativeFirstInstallPeer is uncalled by runtime code. A successful check
// only constructs the unexported ambient #041 observation. It does not
// persist credentials, create grants, or authorize continuing hosted use.
func newNativeFirstInstallPeer(cfg nativeFirstInstallPeerConfig) (nativeInstallVerifier, error) {
	endpoint, ok := nativePeerEndpoint(cfg.Endpoint, cfg.BillingName)
	if !ok || !nativePeerDNS(cfg.BillingName) || !nativePeerIdentifier(cfg.Peer.Issuer) ||
		!nativePeerIdentifier(cfg.Peer.Recipient) || cfg.Peer.Issuer == cfg.Peer.Recipient || !nativePeerIdentifier(cfg.Audience) ||
		cfg.Roots == nil || cfg.BillingSPKI == ([32]byte{}) || len(cfg.ClientIdentity.Certificate) == 0 || cfg.ClientIdentity.PrivateKey == nil {
		return nil, nativeOwnerConsentUnavailable
	}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, DisableKeepAlives: true,
		DialContext:         (&net.Dialer{Timeout: nativePeerTimeout}).DialContext,
		TLSHandshakeTimeout: nativePeerTimeout, ResponseHeaderTimeout: nativePeerTimeout,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: cfg.Roots.Clone(), ServerName: cfg.BillingName,
			Certificates: []tls.Certificate{cfg.ClientIdentity},
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 ||
					sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo) != cfg.BillingSPKI {
					return nativeOwnerConsentUnavailable
				}
				return nil
			},
		},
	}
	client := &http.Client{Transport: transport, Timeout: nativePeerTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, request nativeInstallRequest) (nativeInstallObservation, error) {
		deny := func() (nativeInstallObservation, error) {
			return nativeInstallObservation{}, nativeOwnerConsentUnavailable
		}
		binding, canonical, ok := nativePeerCanonicalRequest(cfg, request)
		if !ok {
			return deny()
		}
		started := nativeOwnerNow()
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(canonical))
		if err != nil {
			return deny()
		}
		r.Host = cfg.BillingName
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Cache-Control", "no-store")
		response, err := client.Do(r)
		if err != nil {
			return deny()
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || !nativePeerJSON(response.Header) || len(response.Header.Values("Content-Encoding")) != 0 ||
			len(response.Header.Values("Cache-Control")) != 1 || !strings.EqualFold(response.Header.Get("Cache-Control"), "no-store") {
			return deny()
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, nativePeerReplyLimit+1))
		reply, ok := decodeNativePeerReply(data)
		if err != nil || !ok || reply.Schema != nativePeerSchema || reply.Issuer != cfg.Peer.Issuer || reply.Recipient != cfg.Peer.Recipient ||
			reply.Purpose != nativeInstallPurpose || reply.Digest != nativePeerDigest(canonical) || reply.Nonce != binding.Nonce {
			return deny()
		}
		checked, a := nativePeerTime(reply.CheckedAt)
		expires, b := nativePeerTime(reply.ExpiresAt)
		now := nativeOwnerNow()
		if ctx.Err() != nil || !a || !b || checked.Before(started) || checked.After(now) || !now.Before(expires) ||
			!now.Before(started.Add(nativeInstallWindow)) || expires.After(checked.Add(nativeInstallWindow)) {
			return deny()
		}
		return nativeInstallObservation{Binding: binding, CheckedAt: checked, ExpiresAt: expires}, nil
	}, nil
}
