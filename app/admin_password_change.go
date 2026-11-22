package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	adminPasswordPath = "/admin/password_change/"
	adminPasswordDone = adminPasswordPath + "done/"
	requiredPassword  = "Este campo es obligatorio."
	nullPassword      = "No se admiten caracteres nulos."
	wrongOldPassword  = "La antigua contraseña introducida es incorrecta. Por favor introdúzcala nuevamente."
	mismatchPassword  = "Los dos campos de contraseñas no coinciden entre si."
	shortPassword     = "La contraseña es demasiado corta. Debe contener por lo menos 8 caracteres."
	commonPassword    = "La contraseña tiene un valor demasiado común."
	numericPassword   = "La contraseña está formada completamente por dígitos."
	similarPassword   = "La contraseña es muy similar a nombre de usuario."
)

var errPasswordStateChanged = errors.New("password-change session or account changed")

type adminPasswordForm struct {
	Old, New, Confirm []string
}

func (f adminPasswordForm) ErrorCount() int {
	count := 0
	for _, field := range [][]string{f.Old, f.New, f.Confirm} {
		if len(field) != 0 {
			count++
		}
	}
	return count
}

func validatePasswordField(value string) []string {
	switch {
	case value == "":
		return []string{requiredPassword}
	case strings.ContainsRune(value, 0):
		return []string{nullPassword}
	case !utf8.ValidString(value):
		return []string{"El valor contiene texto no válido."}
	}
	return nil
}

func validatePasswordChange(u user, old, new1, new2 string) adminPasswordForm {
	form := adminPasswordForm{
		Old: validatePasswordField(old), New: validatePasswordField(new1), Confirm: validatePasswordField(new2),
	}
	if len(form.Old) == 0 && !verifyPassword(u.password, old) {
		form.Old = []string{wrongOldPassword}
	}
	if len(form.Confirm) == 0 {
		if len(form.New) == 0 && new1 != new2 {
			form.Confirm = []string{mismatchPassword}
		} else {
			form.Confirm = validateNewAdminPassword(new2, u.name)
		}
	}
	return form
}

func lastPasswordField(form url.Values, name string) string {
	if values := form[name]; len(values) != 0 {
		return values[len(values)-1]
	}
	return ""
}

func (a *app) serveAdminPassword(w http.ResponseWriter, r *http.Request, u user) {
	done := r.URL.Path == adminPasswordDone
	allow := "GET, POST, PUT, HEAD, OPTIONS"
	if done {
		allow = "GET, HEAD, OPTIONS"
	}
	page := adminPage{Username: u.name, Links: adminLists}
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Allow", allow)
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodGet, http.MethodHead:
	case http.MethodPost, http.MethodPut:
		if !done {
			break
		}
		fallthrough
	default:
		w.Header().Set("Allow", allow)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if done {
		page.Title = "Cambio de contraseña exitoso"
		page.PasswordDone = true
	} else {
		page.Title = "Cambio de contraseña"
		form := adminPasswordForm{}
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			// Django binds only POST form values. A PUT has an empty form.
			old := lastPasswordField(r.PostForm, "old_password")
			new1 := lastPasswordField(r.PostForm, "new_password1")
			new2 := lastPasswordField(r.PostForm, "new_password2")
			form = validatePasswordChange(u, old, new1, new2)
			if form.ErrorCount() == 0 {
				token, err := a.changeAdminPassword(r.Context(), cookieValue(r, a.adminSessionCookie()), u, encodePassword(new1))
				if errors.Is(err, errPasswordStateChanged) {
					redirectAdminLogin(w, r)
					return
				}
				if err != nil {
					failure(w, err)
					return
				}
				// Keep the existing CSRF secret. This is not a new login.
				a.updateAdminPasswordCookie(w, r, token)
				http.Redirect(w, r, adminPasswordDone, http.StatusFound)
				return
			}
		}
		page.PasswordForm = &form // Only errors, never submitted passwords.
	}
	page.Token = a.ensureAdminCSRF(w, r)
	renderAdmin(w, page)
}

func (a *app) changeAdminPassword(ctx context.Context, oldToken string, u user, newEncoded string) (string, error) {
	// Table names are fixed by server mode, never request input. Use the
	// existing optimistic checks for both realms.
	sessionTable, privileged, age := "sessions", "superuser=TRUE", sessionAge
	if a.adminRecovery {
		sessionTable, privileged, age = "admin_recovery_sessions", "staff=TRUE AND superuser=TRUE", adminRecoveryAge
	}
	newToken := randomToken()
	oldKey, newKey := sha256.Sum256([]byte(oldToken)), sha256.Sum256([]byte(newToken))
	oldDigest, newDigest := sha256.Sum256([]byte(u.password)), sha256.Sum256([]byte(newEncoded))
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now()
	// Check that both the active superuser and the initiating session still
	// match the authenticated snapshot after verifying/encoding outside the
	// lock. Never update another user's password or another session row.
	if err := passwordChangeRow(tx.ExecContext(ctx, `UPDATE users SET password=$1
		WHERE id=$2 AND active=TRUE AND `+privileged+` AND username=$3 AND password=$4
		AND EXISTS (SELECT 1 FROM `+sessionTable+` WHERE token_hash=$5 AND user_id=users.id
			AND password_digest=$6 AND expires>$7)`,
		newEncoded, u.id, u.name, u.password, oldKey[:], oldDigest[:], now.Unix())); err != nil {
		return "", err
	}
	if err := passwordChangeRow(tx.ExecContext(ctx, `UPDATE `+sessionTable+`
		SET token_hash=$1, password_digest=$2, expires=$3
		WHERE token_hash=$4 AND user_id=$5 AND password_digest=$6`,
		newKey[:], newDigest[:], now.Add(age).Unix(), oldKey[:], u.id, oldDigest[:])); err != nil {
		return "", err
	}
	// Peers retain their old digests and are invalidated on next use, as in
	// Django. The account's last_login is untouched.
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return newToken, nil
}

func passwordChangeRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errPasswordStateChanged
	}
	return nil
}
