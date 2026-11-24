package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	nativePeerFixtureBilling   = "billing-fixture.qa.invalid"
	nativePeerFixturePath      = "/fixture/first-install"
	nativePeerFixtureCanonical = `{"schema":1,"issuer":"gomar-fixture","recipient":"billing-fixture","purpose":"first-install-observation","audience":"hosted-fixture","intent":"intent-fixture","business":"business-fixture","initiator":"fixture-opener","claimant":"fixture-claimant","branches":["north","south_2"],"request_ref":"reservation-fixture","acceptance_ref":"accept-ISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0-P0A","designation_version":7,"nonce":"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"}`
	// From independent Python stdlib compact JSON + SHA-256, using the
	// specified UTF-8 prefix, NUL separator, and literal v1 field sequence.
	// Neither the producer fixture nor this vector uses a Billing Go type.
	nativePeerFixtureDigest = "4394124b4aeaccf7b38f548de9018fdbc2dbad183b215d48bc56940afa3975a1"
	nativePeerFixtureNonce  = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
)

func nativePeerFixtureRequest() nativeInstallRequest {
	return nativeInstallRequest{Peer: nativeInstallPeer{Issuer: "billing-fixture", Recipient: "gomar-fixture"}, Schema: 1, Purpose: "first-install-observation",
		Target: nativeOwnerTarget{Intent: "intent-fixture", Business: "business-fixture", Audience: "hosted-fixture", RequestRef: "reservation-fixture",
			Revision: 7, Outlets: []string{"south_2", "north"}}, Initiator: "fixture-opener", Claimant: "fixture-claimant",
		ConsentRef: "accept-ISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0-P0A", ApprovalRef: "approval-QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpbXF1eX2A",
		Nonce: nativePeerFixtureNonce}
}

func TestNativeFirstInstallPeerCanonicalGolden(t *testing.T) {
	req := nativePeerFixtureRequest()
	cfg := nativeFirstInstallPeerConfig{Peer: req.Peer, Audience: req.Target.Audience}
	binding, canonical, ok := nativePeerCanonicalRequest(cfg, req)
	if !ok || string(canonical) != nativePeerFixtureCanonical || nativePeerDigest(canonical) != nativePeerFixtureDigest {
		t.Fatal("independent v1 request golden no longer matches")
	}
	if binding.ApprovalRef != req.ApprovalRef || strings.Contains(string(canonical), req.ApprovalRef) ||
		binding.Target.Outlets[0] != "north" || req.Target.Outlets[0] != "south_2" {
		t.Fatal("wrong local-only approval or canonical scope")
	}
}

type nativePeerTestCA struct {
	cert  *x509.Certificate
	key   ed25519.PrivateKey
	roots *x509.CertPool
	now   time.Time
}

func newNativePeerTestCA(t *testing.T, serial int64) nativePeerTestCA {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign}
	raw, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return nativePeerTestCA{cert, key, pool, now}
}

func (ca nativePeerTestCA) leaf(t *testing.T, serial int64, name string, use x509.ExtKeyUsage) (tls.Certificate, [32]byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: ca.now.Add(-time.Minute), NotAfter: ca.now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{use}, DNSNames: []string{name}}
	raw, err := x509.CreateCertificate(rand.Reader, template, ca.cert, pub, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key}, sha256.Sum256(cert.RawSubjectPublicKeyInfo)
}

func TestNativeFirstInstallPeerMutualTLS(t *testing.T) {
	billingCA := newNativePeerTestCA(t, 1)
	nativeCA := newNativePeerTestCA(t, 2)
	untrustedCA := newNativePeerTestCA(t, 3)
	server, serverPin := billingCA.leaf(t, 4, nativePeerFixtureBilling, x509.ExtKeyUsageServerAuth)
	native, nativePin := nativeCA.leaf(t, 5, "native-fixture.qa.invalid", x509.ExtKeyUsageClientAuth)
	wrongPin, _ := billingCA.leaf(t, 6, nativePeerFixtureBilling, x509.ExtKeyUsageServerAuth)
	badName, badNamePin := billingCA.leaf(t, 7, "some-other-billing.qa.invalid", x509.ExtKeyUsageServerAuth)
	badRoot, badRootPin := untrustedCA.leaf(t, 8, nativePeerFixtureBilling, x509.ExtKeyUsageServerAuth)
	wrongNative, _ := untrustedCA.leaf(t, 9, "native-fixture.qa.invalid", x509.ExtKeyUsageClientAuth)
	accepted := nativePeerFixtureRequest()
	var requests, redirects, proxies atomic.Int64
	var mode atomic.Value
	mode.Store("exact")
	fixtureReply := func(now time.Time) map[string]any {
		return map[string]any{"schema": 1, "issuer": "billing-fixture", "recipient": "gomar-fixture", "purpose": "first-install-observation",
			"binding_sha256": nativePeerFixtureDigest, "nonce": nativePeerFixtureNonce,
			"checked_at": now.UTC().Format(time.RFC3339Nano), "expires_at": now.Add(2 * time.Second).UTC().Format(time.RFC3339Nano)}
	}
	testServer := func(c tls.Certificate) *httptest.Server {
		s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/not-billing" {
				redirects.Add(1)
				http.Error(w, "unexpected redirect", 500)
				return
			}
			requests.Add(1)
			d, tlsState := r.Header, r.TLS
			if r.Method != "POST" || r.Host != nativePeerFixtureBilling || r.URL.Path != nativePeerFixturePath || r.URL.RawQuery != "" ||
				d.Get("Cookie") != "" || d.Get("Authorization") != "" || d.Get("Content-Type") != "application/json" ||
				tlsState == nil || len(tlsState.VerifiedChains) == 0 || len(tlsState.PeerCertificates) == 0 ||
				sha256.Sum256(tlsState.PeerCertificates[0].RawSubjectPublicKeyInfo) != nativePin {
				t.Error("request did not stay within pinned fixture, or sent browser credentials")
				http.Error(w, "unavailable", 404)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
			if err != nil || !bytes.Equal(body, []byte(nativePeerFixtureCanonical)) {
				t.Error("actual TLS body disagreed with independent canonical bytes")
				http.Error(w, "unavailable", 404)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			now := nativeOwnerNow()
			reply := fixtureReply(now)
			v := mode.Load().(string)
			switch v {
			case "redirect":
				w.Header().Set("Location", "/not-billing")
				w.WriteHeader(http.StatusTemporaryRedirect)
				return
			case "unavailable":
				http.Error(w, "details must not reach caller", http.StatusNotFound)
				return
			case "timeout":
				<-r.Context().Done()
				return
			case "cookie":
				w.Header().Set("Set-Cookie", "synthetic-cookie=not-authority")
			case "malformed":
				_, _ = io.WriteString(w, "not JSON")
				return
			case "large":
				_, _ = io.WriteString(w, strings.Repeat(" ", nativePeerReplyLimit+1))
				return
			case "wrong-media":
				w.Header().Set("Content-Type", "text/plain")
			case "compressed":
				w.Header().Set("Content-Encoding", "gzip")
			case "cacheable":
				w.Header().Set("Cache-Control", "public")
			case "wrong-issuer":
				reply["issuer"] = "foreign"
			case "wrong-recipient":
				reply["recipient"] = "foreign"
			case "wrong-purpose":
				reply["purpose"] = "hosted-use"
			case "wrong-schema":
				reply["schema"] = 9
			case "wrong-binding":
				reply["binding_sha256"] = strings.Repeat("0", 64)
			case "wrong-nonce":
				reply["nonce"] = "another"
			case "unknown":
				reply["provider_data"] = "synthetic-extra"
			case "missing":
				delete(reply, "expires_at")
			case "null":
				reply["checked_at"] = nil
			case "case-folded":
				reply["Nonce"] = reply["nonce"]
				delete(reply, "nonce")
			case "stale":
				reply = fixtureReply(now.Add(-time.Second))
			case "future":
				reply = fixtureReply(now.Add(time.Second))
			case "too-long":
				reply["expires_at"] = now.Add(nativeInstallWindow + time.Millisecond).Format(time.RFC3339Nano)
			case "non-utc":
				reply["checked_at"] = strings.TrimSuffix(now.Format(time.RFC3339Nano), "Z") + "+00:00"
			case "expired":
				expiry := now.Add(40 * time.Millisecond)
				reply["expires_at"] = expiry.Format(time.RFC3339Nano)
				time.Sleep(max(0, time.Until(expiry)) + 10*time.Millisecond)
			}
			raw, err := json.Marshal(reply) // independent literal keys/fields
			if err != nil {
				t.Error(err)
				return
			}
			if v == "duplicate" {
				raw = append(raw[:len(raw)-1], []byte(`,"schema":1}`)...)
			}
			if v == "trailing" {
				raw = append(raw, []byte(" {}")...)
			}
			_, _ = w.Write(raw)
		}))
		s.Config.ErrorLog = log.New(io.Discard, "", 0)
		s.Config.ReadHeaderTimeout = time.Second
		s.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{c}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: nativeCA.roots}
		s.StartTLS()
		t.Cleanup(s.Close)
		return s
	}
	srv := testServer(server)
	spy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxies.Add(1)
		http.Error(w, "unexpected proxy", 500)
	}))
	defer spy.Close()
	t.Setenv("HTTPS_PROXY", spy.URL)
	t.Setenv("HTTP_PROXY", spy.URL)
	t.Setenv("NO_PROXY", "")
	cfg := nativeFirstInstallPeerConfig{Endpoint: srv.URL + nativePeerFixturePath, BillingName: nativePeerFixtureBilling, Peer: accepted.Peer,
		Audience: accepted.Target.Audience, Roots: billingCA.roots, BillingSPKI: serverPin, ClientIdentity: native}
	deny := func(got nativeInstallObservation, err error) {
		t.Helper()
		if !errors.Is(err, nativeOwnerConsentUnavailable) || got.Binding.Nonce != "" || !got.CheckedAt.IsZero() || !got.ExpiresAt.IsZero() {
			t.Fatal("denial revealed reason or conveyed an observation")
		}
	}
	for _, change := range []func(*nativeFirstInstallPeerConfig){
		func(p *nativeFirstInstallPeerConfig) { p.Endpoint = "http://billing-fixture.qa.invalid/fixture" },
		func(p *nativeFirstInstallPeerConfig) { p.Endpoint += "?other=1" },
		func(p *nativeFirstInstallPeerConfig) { p.Endpoint += "#anchor" },
		func(p *nativeFirstInstallPeerConfig) { p.Endpoint = srv.URL + "/fixture/../not-billing" },
		func(p *nativeFirstInstallPeerConfig) { p.Endpoint = srv.URL + "/fixture/%2finstall" },
		func(p *nativeFirstInstallPeerConfig) { p.Endpoint = "https://user@billing-fixture.qa.invalid/fixture" },
		func(p *nativeFirstInstallPeerConfig) { p.Endpoint = "https://other.qa.invalid/fixture" },
		func(p *nativeFirstInstallPeerConfig) { p.Roots = nil },
		func(p *nativeFirstInstallPeerConfig) { p.BillingSPKI = [32]byte{} },
		func(p *nativeFirstInstallPeerConfig) { p.ClientIdentity = tls.Certificate{} },
	} {
		bad := cfg
		change(&bad)
		if v, err := newNativeFirstInstallPeer(bad); v != nil || !errors.Is(err, nativeOwnerConsentUnavailable) {
			t.Fatal("invalid fixed destination or trust accepted")
		}
	}
	for _, tc := range []struct {
		leaf tls.Certificate
		pin  [32]byte
	}{{wrongPin, serverPin}, {badName, badNamePin}, {badRoot, badRootPin}} {
		bad := cfg
		bad.Endpoint = testServer(tc.leaf).URL + nativePeerFixturePath
		bad.BillingSPKI = tc.pin
		v, err := newNativeFirstInstallPeer(bad)
		if err != nil {
			t.Fatal(err)
		}
		deny(v(context.Background(), accepted))
	}
	otherIdentity := cfg
	otherIdentity.ClientIdentity = wrongNative
	v, err := newNativeFirstInstallPeer(otherIdentity)
	if err != nil {
		t.Fatal(err)
	}
	deny(v(context.Background(), accepted))
	v, err = newNativeFirstInstallPeer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*nativeInstallRequest){
		func(r *nativeInstallRequest) { r.Peer.Issuer = "other-billing" },
		func(r *nativeInstallRequest) { r.Target.Audience = "another-instance" },
		func(r *nativeInstallRequest) { r.Purpose = "hosted-use" },
		func(r *nativeInstallRequest) { r.Nonce = "wrong" },
		func(r *nativeInstallRequest) { r.Target.Outlets = []string{"north", "north"} },
	} {
		bad := accepted
		mutate(&bad)
		deny(v(context.Background(), bad))
	}
	if requests.Load() != 0 {
		t.Fatal("untrusted or wrongly configured attempt reached fixture handler")
	}
	for _, bad := range []string{"redirect", "unavailable", "malformed", "large", "wrong-media", "compressed", "cacheable",
		"wrong-issuer", "wrong-recipient", "wrong-purpose", "wrong-schema", "wrong-binding", "wrong-nonce",
		"unknown", "missing", "null", "case-folded", "duplicate", "trailing", "stale", "future", "too-long", "non-utc", "expired"} {
		t.Run(bad, func(t *testing.T) {
			mode.Store(bad)
			deny(v(context.Background(), accepted))
		})
	}
	mode.Store("timeout")
	begun := requests.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	deny(v(ctx, accepted))
	cancel()
	if requests.Load() != begun+1 {
		t.Fatal("actual mounted handler did not exercise timeout")
	}
	for _, good := range []string{"cookie", "exact"} {
		mode.Store(good)
		got, err := v(context.Background(), accepted)
		expect := accepted
		expect.Target.Outlets = []string{"north", "south_2"}
		if err != nil || !sameNativeInstallRequest(got.Binding, expect) || got.CheckedAt.IsZero() ||
			got.ExpiresAt.Sub(got.CheckedAt) != 2*time.Second || !nativeOwnerNow().Before(got.ExpiresAt) {
			t.Fatal("expected exact fresh ambient observation with untouched times", err)
		}
	}
	if redirects.Load() != 0 || proxies.Load() != 0 {
		t.Fatal("client followed redirect or contacted environment proxy")
	}
}
