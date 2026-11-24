package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
)

// Only explicitly injected by the HTTPS loopback test fixture. No flag,
// environment setting, or main wiring can enable signup.
type nativeSignupHosted struct {
	Enabled  bool
	Origin   string
	Audience string
}

const (
	nativeSignupStatusPath  = "/api/v1/alta/estado"
	nativeSignupReviewPath  = "/api/v1/alta/revisar"
	nativeSignupConfirmPath = "/api/v1/alta/confirmar"
)

type nativeSignupStatus struct {
	State        nativeSignupState `json:"state"`
	Organization string            `json:"organization,omitempty"`
	Branches     []string          `json:"branches,omitempty"`
}

type nativeSignupReview struct {
	Organization string   `json:"organization"`
	Branches     []string `json:"branches"`
	Challenge    string   `json:"challenge"`
}

type nativeSignupAction struct {
	challenge string
	confirm   bool
}

func nativeSignupLoopback(host string) bool {
	address, err := netip.ParseAddr(host)
	return err == nil && address.Zone() == "" && address.IsLoopback()
}

func (a *app) activeNativeSignup() *nativeSignupHosted {
	cfg := a.nativeSignup
	if cfg == nil || !cfg.Enabled || a.db == nil || !nativeOwnerOpaqueID(cfg.Audience) ||
		a.providerAuth == nil || a.providerAuth.origin != cfg.Origin || a.providerAuth.host != a.host {
		return nil
	}
	origin, err := url.Parse(cfg.Origin)
	if err != nil || cfg.Origin != "https://"+a.host || origin.Host != a.host || origin.Port() == "" || !nativeSignupLoopback(origin.Hostname()) {
		return nil
	}
	return cfg
}

func nativeSignupPrivateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Vary", "Cookie")
}

func (a *app) nativeSignupLocal(r *http.Request, path string) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && nativeSignupLoopback(host) && r.TLS != nil && r.Host == a.host &&
		r.URL.Path == path && r.URL.RawPath == "" && r.URL.RawQuery == "" && !r.URL.ForceQuery &&
		len(r.Header.Values("Authorization")) == 0
}

func (a *app) canOpenNativeSignup(r *http.Request, cfg *nativeSignupHosted) bool {
	cookies := r.CookiesNamed("sessionid")
	if len(cookies) != 1 || len(cookies[0].Value) != 32 {
		return false
	}
	ctx := r.Context()
	session := sha256.Sum256([]byte(cookies[0].Value))
	tx, browser, err := a.beginNativeOwnerBrowser(ctx, session)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	_, err = loadNativeSignupSelection(ctx, tx, browser, cfg.Audience)
	return err == nil
}

// Disallow duplicate, unknown, trailing and non-object input without
// relying on the standard decoder's permissive struct field matching.
func readNativeSignupAction(w http.ResponseWriter, r *http.Request, confirm bool) (nativeSignupAction, error) {
	var result nativeSignupAction
	mimeType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mimeType != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
		return result, badJSON
	}
	body := http.MaxBytesReader(w, r.Body, 1024)
	defer body.Close()
	dec := json.NewDecoder(body)
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return result, badJSON
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err = dec.Token()
		field, ok := tok.(string)
		if err != nil || !ok || !confirm || seen[field] {
			return result, badJSON
		}
		seen[field] = true
		switch field {
		case "challenge":
			err = dec.Decode(&result.challenge)
		case "confirm":
			err = dec.Decode(&result.confirm)
		default:
			return result, badJSON
		}
		if err != nil {
			return result, badJSON
		}
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim('}') {
		return result, badJSON
	}
	var trailing json.RawMessage
	if !errors.Is(dec.Decode(&trailing), io.EOF) {
		return result, badJSON
	}
	if confirm && (!result.confirm || !providerRandom(result.challenge)) {
		return result, badInput
	}
	return result, nil
}

func (a *app) registerNativeSignup(handle func(string, http.HandlerFunc), add func(string, string, http.HandlerFunc), frontend http.Handler) {
	cfg := a.activeNativeSignup()
	if cfg == nil {
		return
	}
	handle("GET /alta", func(w http.ResponseWriter, r *http.Request) {
		nativeSignupPrivateHeaders(w)
		if !a.nativeSignupLocal(r, "/alta") || !a.canOpenNativeSignup(r, cfg) {
			http.NotFound(w, r)
			return
		}
		page := r.Clone(r.Context())
		page.URL.Path = "/"
		frontend.ServeHTTP(w, page)
	})
	for _, path := range []string{nativeSignupStatusPath, nativeSignupReviewPath, nativeSignupConfirmPath} {
		add(path, http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
			nativeSignupPrivateHeaders(w)
			if !a.nativeSignupLocal(r, path) {
				http.NotFound(w, r)
				return
			}
			if err := a.handleNativeSignup(w, r, cfg, path); err != nil {
				failure(w, err)
			}
		})
	}
}

func (a *app) handleNativeSignup(w http.ResponseWriter, r *http.Request, cfg *nativeSignupHosted, path string) error {
	confirm := path == nativeSignupConfirmPath
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return err
	}
	input, err := readNativeSignupAction(w, r, confirm)
	if err != nil {
		return err
	}
	ctx := r.Context()
	tx, browser, err := a.beginNativeOwnerBrowser(ctx, session)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	selected, err := loadNativeSignupSelection(ctx, tx, browser, cfg.Audience)
	if err != nil {
		return err
	}
	plan, d := selected.Plan, selected.Designation
	if path == nativeSignupStatusPath {
		status := nativeSignupStatus{State: selected.State}
		if selected.State != nativeSignupReviewable {
			status.Organization = plan.Organization
			for _, branch := range plan.Branches {
				status.Branches = append(status.Branches, branch.Name)
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, http.StatusOK, status)
		return nil
	}
	if selected.State != nativeSignupReviewable {
		return nativeOwnerConsentUnavailable
	}
	binding := nativeOwnerReviewBinding{Actor: browser.Actor, Target: d.Target,
		Display: nativeOwnerDisplayDigest(browser.Actor, d.Target, plan.Organization, plan.Branches)}
	if confirm {
		if _, err = writeNativeOwnerConfirmation(ctx, tx, browser, d, d.Target, input.challenge, &binding); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	preview, err := writeNativeOwnerPreview(ctx, tx, browser, d, cfg.Audience, &binding)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	visible := nativeSignupReview{Organization: plan.Organization, Challenge: preview.Challenge}
	for _, branch := range plan.Branches {
		visible.Branches = append(visible.Branches, branch.Name)
	}
	jsonResponse(w, http.StatusOK, visible)
	return nil
}
