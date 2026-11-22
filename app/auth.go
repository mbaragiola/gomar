package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	passwordIterations = 1_000_000
	sessionAge         = 14 * 24 * time.Hour
	csrfAge            = 52 * 7 * 24 * time.Hour
	csrfChars          = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	csrfFailure        = "La verificación de seguridad venció o falta. Recargá la página e intentá de nuevo."
	incorrectLogin     = "Usuario o contraseña incorrectos."
)

type user struct {
	id               int64
	name, password   string
	staff, superuser bool
}

func randomToken() string {
	raw := make([]byte, 20)
	rand.Read(raw)
	return strings.ToLower(base32.StdEncoding.EncodeToString(raw))
}

func derivePassword(password, salt string, iterations int) []byte {
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), iterations, sha256.Size)
	if err != nil {
		panic(err)
	}
	return key
}

func encodePassword(password string) string {
	salt := randomToken()
	key := derivePassword(password, salt, passwordIterations)
	return "pbkdf2_sha256$" + strconv.Itoa(passwordIterations) + "$" + salt + "$" + base64.StdEncoding.EncodeToString(key)
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) == 4 && parts[0] == "pbkdf2_sha256" {
		iterations, err := strconv.Atoi(parts[1])
		key, decodeErr := base64.StdEncoding.Strict().DecodeString(parts[3])
		if err == nil && iterations > 0 && iterations <= 4*passwordIterations && decodeErr == nil && len(key) == sha256.Size {
			return subtle.ConstantTimeCompare(key, derivePassword(password, parts[2], iterations)) == 1
		}
	}
	// Match the default work factor for an unknown user or unusable hash.
	derivePassword(password, "foundation-unusable-password", passwordIterations)
	return false
}

func scanUser(row *sql.Row) (user, error) {
	var u user
	err := row.Scan(&u.id, &u.name, &u.password, &u.staff, &u.superuser)
	if errors.Is(err, sql.ErrNoRows) {
		return user{}, nil
	}
	return u, err
}

func (a *app) authenticate(ctx context.Context, username, password string) (user, error) {
	u, err := scanUser(a.db.QueryRowContext(ctx, `SELECT id,username,password,staff,superuser
		FROM users WHERE username=$1 AND active=TRUE`, username))
	if err != nil {
		return user{}, err
	}
	if !verifyPassword(u.password, password) {
		return user{}, nil
	}
	return u, nil
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

type sessionStore interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Resolve the current principal, not a cached role. A disabled user stays
// signed out without deleting their session, matching Django's model backend.
func currentUser(w http.ResponseWriter, r *http.Request, db sessionStore) (user, error) {
	token := cookieValue(r, "sessionid")
	if token == "" {
		return user{}, nil
	}
	clearCookie := func() {
		if w != nil {
			setCookie(w, r, "sessionid", "", 0, false)
		}
	}
	if len(token) != 32 {
		clearCookie()
		return user{}, nil
	}
	key := sha256.Sum256([]byte(token))
	var saved []byte
	var u user
	var active bool
	err := db.QueryRowContext(r.Context(), `SELECT u.id,u.username,u.password,u.staff,u.superuser,u.active,s.password_digest
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.expires>$2`, key[:], time.Now().Unix()).
		Scan(&u.id, &u.name, &u.password, &u.staff, &u.superuser, &active, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		clearCookie()
		return user{}, nil
	}
	if err != nil {
		return user{}, err
	}
	if !active {
		return user{}, nil
	}
	digest := sha256.Sum256([]byte(u.password))
	if subtle.ConstantTimeCompare(saved, digest[:]) != 1 {
		// Revocation survives restoring the old credential. The old digest
		// condition prevents deleting a newer row written in the meantime.
		if _, err := db.ExecContext(r.Context(),
			"DELETE FROM sessions WHERE token_hash=$1 AND password_digest=$2", key[:], saved); err != nil {
			return user{}, err
		}
		clearCookie()
		return user{}, nil
	}
	return u, nil
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, age time.Duration, httpOnly bool) {
	w.Header().Add("Vary", "Cookie")
	cookie := &http.Cookie{
		Name: name, Value: value, Path: "/", Secure: effectiveHTTPS(r),
		HttpOnly: httpOnly, SameSite: http.SameSiteLaxMode,
		MaxAge: int(age.Seconds()), Expires: time.Now().Add(age),
	}
	if age == 0 {
		cookie.MaxAge, cookie.Expires = -1, time.Unix(0, 0)
		// Django's deletion cookie drops HttpOnly.
		cookie.HttpOnly = false
		if isPrivateHTTPS(r) {
			// Pinned Django drops Secure on the empty session tombstone.
			cookie.Secure = false
		}
	}
	http.SetCookie(w, cookie)
}

func (a *app) startSession(w http.ResponseWriter, r *http.Request, u user) error {
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := currentUser(nil, r, tx)
	if err != nil {
		return err
	}
	old := cookieValue(r, "sessionid")
	token := old
	if current.id != u.id {
		token = randomToken()
	}
	now := time.Now()
	if old != "" {
		key := sha256.Sum256([]byte(old))
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=$1", key[:]); err != nil {
			return err
		}
	}
	key := sha256.Sum256([]byte(token))
	digest := sha256.Sum256([]byte(u.password))
	result, err := tx.ExecContext(ctx, "UPDATE users SET last_login=$1 WHERE id=$2 AND active=TRUE AND password=$3", now.Unix(), u.id, u.password)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("login account changed")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,user_id,password_digest,expires) VALUES ($1,$2,$3,$4)",
		key[:], u.id, digest[:], now.Add(sessionAge).Unix()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	setCookie(w, r, "sessionid", token, sessionAge, true)
	setCookie(w, r, "csrftoken", randomToken(), csrfAge, false)
	return nil
}

func (a *app) endSession(w http.ResponseWriter, r *http.Request) error {
	if token := cookieValue(r, "sessionid"); token != "" {
		key := sha256.Sum256([]byte(token))
		if _, err := a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", key[:]); err != nil {
			return err
		}
		setCookie(w, r, "sessionid", "", 0, false)
	}
	return nil
}

func csrfSecret(token string) string {
	if len(token) != 32 && len(token) != 64 {
		return ""
	}
	for _, ch := range token {
		if !strings.ContainsRune(csrfChars, ch) {
			return ""
		}
	}
	if len(token) == 32 {
		return token
	}
	secret := make([]byte, 32)
	for i := range secret {
		mask := strings.IndexByte(csrfChars, token[i])
		masked := strings.IndexByte(csrfChars, token[i+32])
		secret[i] = csrfChars[(masked-mask+len(csrfChars))%len(csrfChars)]
	}
	return string(secret)
}

func ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	secret := csrfSecret(cookieValue(r, "csrftoken"))
	if secret == "" {
		secret = randomToken()
	}
	setCookie(w, r, "csrftoken", secret, csrfAge, false)
	return secret
}

func validCSRF(r *http.Request) bool {
	origin := "http://" + r.Host
	if effectiveHTTPS(r) {
		origin = "https://" + r.Host
	}
	if origins, present := r.Header["Origin"]; present {
		if len(origins) != 1 || origins[0] != origin {
			return false
		}
	} else if isPrivateHTTPS(r) {
		referers := r.Header.Values("Referer")
		if len(referers) != 1 {
			return false
		}
		referer, err := url.ParseRequestURI(referers[0])
		if err != nil || referer.Scheme != "https" || referer.Host != r.Host || referer.User != nil || referer.Opaque != "" {
			return false
		}
	} else if r.TLS != nil {
		referer := r.Referer()
		if referer != origin && !strings.HasPrefix(referer, origin+"/") {
			return false
		}
	}
	secret := csrfSecret(cookieValue(r, "csrftoken"))
	if secret == "" {
		return false
	}
	token := r.PostForm.Get("csrfmiddlewaretoken")
	if token == "" {
		token = r.Header.Get("X-CSRFToken")
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(csrfSecret(token))) == 1
}
