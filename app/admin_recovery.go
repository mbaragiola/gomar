package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const adminRecoveryMigration = "019_admin_recovery.sql"
const adminRecoveryCookie = "__Secure-gomar_admin"
const adminRecoveryCSRF = "__Secure-gomar_admin_csrf"
const adminRecoveryAge = 30 * time.Minute

var errAdminRecoverySchema = errors.New("admin recovery requires its registered and applied PostgreSQL migration")
var errAdminRecoveryHTTPS = errors.New("admin recovery requires protected HTTPS Unix ingress")
var errProviderOnlyUnsafe = errors.New("provider-only sign-in requires isolated admin recovery and configured Google and Microsoft personal providers")

func validateSignInMode(recovery, providerOnly, protectedIngress bool, migrations []string, providers *hostedProviderAuth) error {
	if recovery {
		if !protectedIngress {
			return errAdminRecoveryHTTPS
		}
		if !slices.Contains(migrations, adminRecoveryMigration) {
			return errAdminRecoverySchema
		}
	}
	if providerOnly && (!recovery || providers == nil || providers.providers["google"] == nil ||
		providers.providers["microsoft"] == nil ||
		!slices.Contains(migrations, providerAuthMigration)) {
		return errProviderOnlyUnsafe
	}
	return nil
}

func setAdminRecoveryCookie(w http.ResponseWriter, name, value string, age time.Duration) {
	maxAge := int(age.Seconds())
	expires := time.Now().Add(age)
	if age == 0 {
		maxAge, expires = -1, time.Unix(0, 0)
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/admin/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Expires: expires})
}

func (a *app) ensureAdminCSRF(w http.ResponseWriter, r *http.Request) string {
	if !a.adminRecovery {
		return ensureCSRF(w, r)
	}
	secret := csrfSecret(cookieValue(r, adminRecoveryCSRF))
	if secret == "" {
		secret = randomToken()
	}
	setAdminRecoveryCookie(w, adminRecoveryCSRF, secret, adminRecoveryAge)
	return secret
}

func (a *app) validAdminCSRF(r *http.Request) bool {
	if !a.adminRecovery {
		return validCSRF(r)
	}
	return effectiveHTTPS(r) && validCSRFWithCookie(r, adminRecoveryCSRF)
}

func (a *app) adminSessionCookie() string {
	if a.adminRecovery {
		return adminRecoveryCookie
	}
	return "sessionid"
}

func (a *app) updateAdminPasswordCookie(w http.ResponseWriter, r *http.Request, token string) {
	if a.adminRecovery {
		setAdminRecoveryCookie(w, adminRecoveryCookie, token, adminRecoveryAge)
		return
	}
	setCookie(w, r, "sessionid", token, sessionAge, true)
}

func (a *app) currentAdminUser(w http.ResponseWriter, r *http.Request) (user, error) {
	if !a.adminRecovery {
		return currentUser(w, r, a.db)
	}
	var u user
	raw := cookieValue(r, adminRecoveryCookie)
	if len(raw) != 32 {
		return u, nil
	}
	digest := sha256.Sum256([]byte(raw))
	var stored []byte
	err := a.db.QueryRowContext(r.Context(), `SELECT u.id,u.username,u.password,u.staff,u.superuser,s.password_digest
		FROM admin_recovery_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.expires>$2 AND u.active AND u.staff AND u.superuser AND u.password<>$3`,
		digest[:], time.Now().Unix(), providerOnlyPassword).
		Scan(&u.id, &u.name, &u.password, &u.staff, &u.superuser, &stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return user{}, err
	}
	expected := sha256.Sum256([]byte(u.password))
	if err != nil || subtle.ConstantTimeCompare(stored, expected[:]) != 1 {
		if _, err = a.db.ExecContext(r.Context(), `DELETE FROM admin_recovery_sessions WHERE token_hash=$1`, digest[:]); err != nil {
			return user{}, err
		}
		if w != nil {
			setAdminRecoveryCookie(w, adminRecoveryCookie, "", 0)
		}
		return user{}, nil
	}
	return u, nil
}

func (a *app) startAdminSession(w http.ResponseWriter, r *http.Request, u user) error {
	if !a.adminRecovery {
		return a.startSession(w, r, u)
	}
	if u.id <= 0 || !u.staff || !u.superuser || u.password == providerOnlyPassword {
		return unauthenticated
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	// Recheck privilege and secret through the account write lock before
	// issuing a recovery session. No ordinary application session is minted.
	if err = passwordChangeRow(tx.ExecContext(ctx, `UPDATE users SET last_login=$1
		WHERE id=$2 AND active AND staff AND superuser AND password=$3`, now.Unix(), u.id, u.password)); err != nil {
		if errors.Is(err, errPasswordStateChanged) {
			return unauthenticated
		}
		return err
	}
	if old := cookieValue(r, adminRecoveryCookie); len(old) == 32 {
		oldHash := sha256.Sum256([]byte(old))
		if _, err = tx.ExecContext(ctx, `DELETE FROM admin_recovery_sessions WHERE token_hash=$1`, oldHash[:]); err != nil {
			return err
		}
	}
	// Keep recovery rows bounded even if a browser never signs out.
	if _, err = tx.ExecContext(ctx, `DELETE FROM admin_recovery_sessions WHERE expires<=$1`, now.Unix()); err != nil {
		return err
	}
	raw := randomToken()
	token, password := sha256.Sum256([]byte(raw)), sha256.Sum256([]byte(u.password))
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_recovery_sessions(token_hash,user_id,password_digest,expires)
		VALUES($1,$2,$3,$4)`, token[:], u.id, password[:], now.Add(adminRecoveryAge).Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	setAdminRecoveryCookie(w, adminRecoveryCookie, raw, adminRecoveryAge)
	setAdminRecoveryCookie(w, adminRecoveryCSRF, randomToken(), adminRecoveryAge)
	return nil
}

func (a *app) endAdminSession(w http.ResponseWriter, r *http.Request) error {
	if !a.adminRecovery {
		return a.endSession(w, r)
	}
	if raw := cookieValue(r, adminRecoveryCookie); len(raw) == 32 {
		hash := sha256.Sum256([]byte(raw))
		if _, err := a.db.ExecContext(r.Context(), `DELETE FROM admin_recovery_sessions WHERE token_hash=$1`, hash[:]); err != nil {
			return err
		}
	}
	setAdminRecoveryCookie(w, adminRecoveryCookie, "", 0)
	setAdminRecoveryCookie(w, adminRecoveryCSRF, "", 0)
	return nil
}

type providerOnlyContextKey struct{}

func providerOnlyRequest(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), providerOnlyContextKey{}, true))
}

func requiresProviderSession(r *http.Request) bool {
	return r.Context().Value(providerOnlyContextKey{}) == true
}

func verifyLocalAdminRecoveryAccount(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT password FROM users WHERE active AND staff AND superuser AND password<>$1`, providerOnlyPassword)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		if err = rows.Scan(&encoded); err != nil {
			return err
		}
		parts := strings.Split(encoded, "$")
		if len(parts) != 4 || parts[0] != "pbkdf2_sha256" || parts[2] == "" {
			continue
		}
		rounds, numberErr := strconv.Atoi(parts[1])
		digest, decodeErr := base64.StdEncoding.Strict().DecodeString(parts[3])
		if numberErr == nil && rounds > 0 && rounds <= 4*passwordIterations && decodeErr == nil && len(digest) == sha256.Size {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return errors.New("admin recovery requires an existing active local staff superuser password")
}
