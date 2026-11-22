package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const providerFlowAge = 5 * time.Minute
const providerCookiePrefix = "__Host-gomar_oidc_"
const providerOnlyPassword = "!provider-only"

var providerLoginFailure = problem{401, "No se pudo verificar el inicio de sesión. Volvé a intentarlo."}

func (a *app) registerProviderLogin(add func(string, string, http.HandlerFunc)) {
	add("/api/v1/auth/providers", http.MethodGet, a.listProviderLoginMethods)
	if a.providerAuth == nil {
		return
	}
	for flow, p := range a.providerAuth.providers {
		base := "/api/v1/auth/providers/" + flow
		add(base+"/start", http.MethodPost, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
			return a.startProviderLogin(w, r, flow, p)
		}))
		method := http.MethodGet
		if p.endpoints.FormPost {
			method = http.MethodPost
		}
		add(base+"/callback", method, a.providerLoginRoute(func(w http.ResponseWriter, r *http.Request) error {
			return a.finishProviderLogin(w, r, flow, p)
		}))
	}
}

func (a *app) listProviderLoginMethods(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	type choice struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	public := []choice{}
	if p := a.providerAuth; p != nil && effectiveHTTPS(r) && r.Host == p.host {
		for _, c := range []choice{{"google", "Google"}, {"apple", "Apple"}, {"microsoft", "Microsoft"}, {"microsoft-work", "Microsoft laboral o educativo"}} {
			if p.providers[c.ID] != nil {
				public = append(public, c)
			}
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"providers": public, "local_password_enabled": !a.providerOnly, "admin_recovery_enabled": a.adminRecovery && effectiveHTTPS(r)})
}

func (a *app) providerLoginRoute(next func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !effectiveHTTPS(r) || r.Host != a.providerAuth.host {
			failure(w, badCSRF)
			return
		}
		if err := next(w, r); err != nil {
			failure(w, err)
		}
	}
}

func newProviderRandom() string {
	value := make([]byte, 32)
	rand.Read(value)
	return base64.RawURLEncoding.EncodeToString(value)
}

func providerRandom(value string) bool {
	if len(value) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil
}

func providerPKCE(state, binding string) string {
	mac := hmac.New(sha256.New, []byte(binding))
	mac.Write([]byte("gomar-provider-pkce-v1:"))
	mac.Write([]byte(state))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func providerFlowCookie(w http.ResponseWriter, p *providerOIDC, state, value string, discard bool) {
	mode := http.SameSiteLaxMode
	if p.endpoints.FormPost {
		mode = http.SameSiteNoneMode
	}
	age := int(providerFlowAge.Seconds())
	if discard {
		age = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: providerCookiePrefix + state, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: mode,
		MaxAge: age,
	})
}

func (a *app) startProviderLogin(w http.ResponseWriter, r *http.Request, flow string, p *providerOIDC) error {
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		return badInput
	}
	if origin := r.Header.Values("Origin"); len(origin) != 1 || origin[0] != a.providerAuth.origin || !validCSRF(r) {
		return badCSRF
	}
	pending := 0
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, providerCookiePrefix) {
			pending++
		}
	}
	if pending >= 4 {
		return problem{429, "Hay demasiados intentos de inicio de sesión. Esperá unos minutos."}
	}
	state, binding, nonce := newProviderRandom(), newProviderRandom(), newProviderRandom()
	stateHash, browserHash, nonceHash := sha256.Sum256([]byte(state)), sha256.Sum256([]byte(binding)), sha256.Sum256([]byte(nonce))
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	// Serialize the small cap and opportunistic expiry. This prevents
	// forgotten login attempts from growing unbounded before a later sweep.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(39817150041023)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM provider_auth_flows WHERE expires_at<=$1`, now.Unix()); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_auth_flows`).Scan(&n); err != nil {
		return err
	}
	if n >= 1024 {
		return problem{429, "Hay demasiados intentos de inicio de sesión. Esperá unos minutos."}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO provider_auth_flows(state_hash,browser_hash,nonce_hash,provider_flow,created_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6)`, stateHash[:], browserHash[:], nonceHash[:], flow, now.Unix(), now.Add(providerFlowAge).Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	providerFlowCookie(w, p, state, binding, false)
	jsonResponse(w, http.StatusOK, map[string]string{"authorization_url": p.authorizationURL(state, nonce, providerPKCE(state, binding))})
	return nil
}

func providerCallbackValues(w http.ResponseWriter, r *http.Request, p *providerOIDC) (url.Values, error) {
	if p.endpoints.FormPost {
		if r.URL.RawQuery != "" {
			return nil, providerLoginFailure
		}
		m, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || m != "application/x-www-form-urlencoded" {
			return nil, providerLoginFailure
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err = r.ParseForm(); err != nil {
			return nil, providerLoginFailure
		}
		return r.PostForm, nil
	}
	if len(r.URL.RawQuery) > 8192 {
		return nil, providerLoginFailure
	}
	vals, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, providerLoginFailure
	}
	return vals, nil
}

func (a *app) finishProviderLogin(w http.ResponseWriter, r *http.Request, flow string, p *providerOIDC) error {
	vals, err := providerCallbackValues(w, r, p)
	if err != nil {
		return err
	}
	state := vals.Get("state")
	if len(vals["state"]) != 1 || !providerRandom(state) {
		return providerLoginFailure
	}
	cookies := r.CookiesNamed(providerCookiePrefix + state)
	if len(cookies) != 1 || !providerRandom(cookies[0].Value) {
		return providerLoginFailure
	}
	binding := cookies[0].Value
	stateHash, browserHash := sha256.Sum256([]byte(state)), sha256.Sum256([]byte(binding))
	var nonce []byte
	err = a.db.QueryRowContext(r.Context(), `DELETE FROM provider_auth_flows
		WHERE state_hash=$1 AND browser_hash=$2 AND provider_flow=$3 AND expires_at>$4 RETURNING nonce_hash`,
		stateHash[:], browserHash[:], flow, time.Now().Unix()).Scan(&nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return providerLoginFailure
	}
	if err != nil {
		return err
	}
	providerFlowCookie(w, p, state, "", true)
	code := vals.Get("code")
	if len(vals["code"]) != 1 || !validProviderSetting(code, 4096) || vals.Has("error") {
		return providerLoginFailure
	}
	id, err := p.exchange(r.Context(), code, providerPKCE(state, binding), nonce)
	if err != nil {
		return providerLoginFailure
	}
	if err = a.startVerifiedProviderSession(w, r, id); err != nil {
		return err
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

func (a *app) startVerifiedProviderSession(w http.ResponseWriter, r *http.Request, id verifiedProviderIdentity) error {
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	u, err := resolveProviderUser(ctx, tx, id)
	if err != nil {
		return err
	}
	now := time.Now()
	token := randomToken() // Always rotate even when the same user signs in.
	key := sha256.Sum256([]byte(token))
	digest := sha256.Sum256([]byte(u.password))
	if old := cookieValue(r, "sessionid"); len(old) == 32 {
		hash := sha256.Sum256([]byte(old))
		if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=$1`, hash[:]); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET last_login=$1 WHERE id=$2`, now.Unix(), u.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,password_digest,expires,auth_method) VALUES($1,$2,$3,$4,'provider')`,
		key[:], u.id, digest[:], now.Add(sessionAge).Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	setCookie(w, r, "sessionid", token, sessionAge, true)
	setCookie(w, r, "csrftoken", randomToken(), csrfAge, false)
	return nil
}

func resolveProviderUser(ctx context.Context, tx *sql.Tx, id verifiedProviderIdentity) (user, error) {
	var u user
	// Serialize fresh sign-ups for this exact provider subject. We do not
	// look at email, profile, roles, organizations, tenants or groups here.
	// Explicit, named grants in Gomar are its only source of access.
	digest := sha256.Sum256([]byte("gomar-provider-identity-v1\x00" + id.Provider + "\x00" + id.Issuer + "\x00" + id.Subject))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(binary.BigEndian.Uint64(digest[:8]))); err != nil {
		return u, err
	}
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT u.id,u.username,u.password,u.active,u.staff,u.superuser FROM provider_identities i
		JOIN users u ON u.id=i.user_id WHERE i.provider=$1 AND i.issuer=$2 AND i.subject=$3 FOR UPDATE OF u`,
		id.Provider, id.Issuer, id.Subject).Scan(&u.id, &u.name, &u.password, &active, &u.staff, &u.superuser)
	if err == nil {
		if !active || u.staff || u.superuser || u.password != providerOnlyPassword {
			return user{}, providerLoginFailure
		}
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return u, err
	}
	u.name, u.password = "oidc-"+randomToken(), providerOnlyPassword
	err = tx.QueryRowContext(ctx, `INSERT INTO users(username,password,active,staff,superuser)
		VALUES($1,$2,TRUE,FALSE,FALSE) RETURNING id`, u.name, u.password).Scan(&u.id)
	if err != nil {
		return user{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_identities(provider,issuer,subject,user_id,created_at) VALUES($1,$2,$3,$4,$5)`,
		id.Provider, id.Issuer, id.Subject, u.id, time.Now().Unix())
	return u, err
}
