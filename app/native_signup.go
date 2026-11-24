package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
)

// Only explicitly injected by the HTTPS loopback test fixture. No flag,
// environment setting, default resolver or main wiring can enable signup.
type nativeSignupHosted struct {
	Enabled  bool
	Origin   string
	Audience string
	Resolver func(context.Context, int64) (nativeSignupPlan, error)
}

type nativeSignupPlan struct {
	Actor        int64
	Target       nativeOwnerTarget
	Organization string
	Branches     []nativeInstallBranch
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
	if cfg == nil || !cfg.Enabled || cfg.Resolver == nil || !nativeOwnerOpaqueID(cfg.Audience) ||
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

// The browser never supplies an intent, target, scope, role or contact
// identity. The trusted resolver runs after locking the current provider
// user and exact session, but outside any transaction.
func (a *app) resolveNativeSignup(r *http.Request, cfg *nativeSignupHosted, session [32]byte) (nativeSignupPlan, nativeOwnerReviewBinding, error) {
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nativeSignupPlan{}, nativeOwnerReviewBinding{}, err
	}
	defer tx.Rollback()
	actor, expires, err := lockedNativeOwnerSession(ctx, tx, session)
	if err != nil || nativeOwnerNow().Unix() >= expires {
		if err == nil {
			err = unauthenticated
		}
		return nativeSignupPlan{}, nativeOwnerReviewBinding{}, err
	}
	if err = tx.Commit(); err != nil {
		return nativeSignupPlan{}, nativeOwnerReviewBinding{}, err
	}
	plan, err := cfg.Resolver(ctx, actor)
	if err != nil || plan.Actor != actor {
		return nativeSignupPlan{}, nativeOwnerReviewBinding{}, nativeOwnerConsentUnavailable
	}
	target, valid := canonicalNativeOwnerTarget(plan.Target)
	if !valid || target.Audience != cfg.Audience || !nativeInstallName(plan.Organization) || len(plan.Branches) != len(target.Outlets) {
		return nativeSignupPlan{}, nativeOwnerReviewBinding{}, nativeOwnerConsentUnavailable
	}
	plan.Target = target
	plan.Branches = slices.Clone(plan.Branches)
	slices.SortFunc(plan.Branches, func(a, b nativeInstallBranch) int { return strings.Compare(a.Ref, b.Ref) })
	names := make(map[string]bool, len(plan.Branches))
	for i, branch := range plan.Branches {
		if branch.Ref != target.Outlets[i] || !nativeInstallName(branch.Name) || names[branch.Name] {
			return nativeSignupPlan{}, nativeOwnerReviewBinding{}, nativeOwnerConsentUnavailable
		}
		names[branch.Name] = true
	}
	serialized, err := json.Marshal(plan)
	if err != nil {
		return nativeSignupPlan{}, nativeOwnerReviewBinding{}, err
	}
	return plan, nativeOwnerReviewBinding{Target: target, Display: sha256.Sum256(serialized)}, nil
}

func (a *app) canOpenNativeSignup(r *http.Request, cfg *nativeSignupHosted) bool {
	cookies := r.CookiesNamed("sessionid")
	if len(cookies) != 1 || len(cookies[0].Value) != 32 {
		return false
	}
	session := sha256.Sum256([]byte(cookies[0].Value))
	plan, _, err := a.resolveNativeSignup(r, cfg, session)
	if err != nil {
		return false
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false
	}
	defer tx.Rollback()
	actor, expires, err := lockedNativeOwnerSession(ctx, tx, session)
	if err != nil || actor != plan.Actor {
		return false
	}
	d, err := lockCurrentNativeOwnerDesignation(ctx, tx, plan.Target.Intent)
	now := nativeOwnerNow()
	return err == nil && d.Actor == actor && d.approvedAt(now) && now.Unix() < expires &&
		sameNativeOwnerTarget(d.Target, plan.Target)
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

func (a *app) registerNativeSignup(mux *http.ServeMux, add func(string, string, http.HandlerFunc), frontend http.Handler) {
	cfg := a.activeNativeSignup()
	if cfg == nil {
		return
	}
	mux.HandleFunc("GET /alta", func(w http.ResponseWriter, r *http.Request) {
		nativeSignupPrivateHeaders(w)
		if !a.nativeSignupLocal(r, "/alta") || !a.canOpenNativeSignup(r, cfg) {
			http.NotFound(w, r)
			return
		}
		page := r.Clone(r.Context())
		page.URL.Path = "/"
		frontend.ServeHTTP(w, page)
	})
	for path, confirm := range map[string]bool{"/api/v1/alta/revisar": false, "/api/v1/alta/confirmar": true} {
		add(path, http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
			nativeSignupPrivateHeaders(w)
			if !a.nativeSignupLocal(r, path) {
				http.NotFound(w, r)
				return
			}
			if err := a.handleNativeSignup(w, r, cfg, confirm); err != nil {
				failure(w, err)
			}
		})
	}
}

func (a *app) handleNativeSignup(w http.ResponseWriter, r *http.Request, cfg *nativeSignupHosted, confirm bool) error {
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return err
	}
	input, err := readNativeSignupAction(w, r, confirm)
	if err != nil {
		return err
	}
	plan, binding, err := a.resolveNativeSignup(r, cfg, session)
	if err != nil {
		return err
	}
	if confirm {
		_, err = a.confirmBoundNativeOwnerConsent(r, nativeOwnerConfirmation{
			Challenge: input.challenge, Target: plan.Target, Accept: true,
		}, &binding)
		if err == nil {
			w.WriteHeader(http.StatusNoContent)
		}
		return err
	}
	preview, err := a.previewBoundNativeOwnerConsent(r, plan.Target.Intent, plan.Target.Audience, &binding)
	if err != nil {
		return err
	}
	visible := nativeSignupReview{Organization: plan.Organization, Challenge: preview.Challenge}
	for _, branch := range plan.Branches {
		visible.Branches = append(visible.Branches, branch.Name)
	}
	jsonResponse(w, http.StatusOK, visible)
	return nil
}
