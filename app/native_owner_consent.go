package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"time"
)

const nativeOwnerConsentLifetime = 5 * time.Minute

var nativeOwnerConsentUnavailable = problem{404, "No hay una solicitud de autorización vigente para esta cuenta y sesión."}

// This contract has no registered route, store, or caller in the deployed app.
// These are opaque scope identifiers, not billing records or local permissions.
type nativeOwnerTarget struct {
	Intent   string   `json:"intent"`
	Business string   `json:"business"`
	Outlets  []string `json:"outlets"`
	Revision int64    `json:"revision"`
}

type nativeOwnerDesignation struct {
	Actor  int64
	Target nativeOwnerTarget
}

type nativeOwnerPreview struct {
	Challenge string            `json:"challenge"`
	Target    nativeOwnerTarget `json:"target"`
}

type nativeOwnerConfirmation struct {
	Challenge string            `json:"challenge"`
	Target    nativeOwnerTarget `json:"target"`
	Accept    bool              `json:"accept"`
}

type nativeOwnerBinding struct {
	Designation nativeOwnerDesignation
	Session     [32]byte
	Expires     time.Time
}

// A future adapter needs authenticated, server-owned designations. Load the
// current designation under lock; never populate it from the browser. All
// methods run in the caller's transaction. Consume must be one-shot, including
// concurrent challenges for the same intent, and retain the exact binding.
type nativeOwnerConsentStore interface {
	lockNativeOwnerDesignation(context.Context, *sql.Tx, string) (nativeOwnerDesignation, error)
	saveNativeOwnerPreview(context.Context, *sql.Tx, [32]byte, nativeOwnerBinding) error
	lockNativeOwnerPreview(context.Context, *sql.Tx, [32]byte) (nativeOwnerBinding, error)
	consumeNativeOwnerPreview(context.Context, *sql.Tx, [32]byte, time.Time) error
}

func nativeOwnerOpaqueID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func canonicalNativeOwnerTarget(target nativeOwnerTarget) (nativeOwnerTarget, bool) {
	if !nativeOwnerOpaqueID(target.Intent) || !nativeOwnerOpaqueID(target.Business) || target.Revision <= 0 ||
		len(target.Outlets) == 0 || len(target.Outlets) > 64 {
		return nativeOwnerTarget{}, false
	}
	target.Outlets = slices.Clone(target.Outlets)
	slices.Sort(target.Outlets)
	for i, outlet := range target.Outlets {
		if !nativeOwnerOpaqueID(outlet) || (i > 0 && outlet == target.Outlets[i-1]) {
			return nativeOwnerTarget{}, false
		}
	}
	return target, true
}

func sameNativeOwnerTarget(a, b nativeOwnerTarget) bool {
	return a.Intent == b.Intent && a.Business == b.Business && a.Revision == b.Revision && slices.Equal(a.Outlets, b.Outlets)
}

func nativeOwnerUnavailable(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nativeOwnerConsentUnavailable
	}
	return err
}

func (a *app) nativeOwnerBrowser(r *http.Request) ([32]byte, error) {
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		return [32]byte{}, badInput
	}
	origin := r.Header.Values("Origin")
	if a.providerAuth == nil || !effectiveHTTPS(r) || r.Host != a.providerAuth.host ||
		len(origin) != 1 || origin[0] != a.providerAuth.origin || len(r.Header.Values("X-CSRFToken")) != 1 ||
		len(r.CookiesNamed("csrftoken")) != 1 || !validCSRF(r) {
		return [32]byte{}, badCSRF
	}
	cookies := r.CookiesNamed("sessionid")
	if len(cookies) != 1 || len(cookies[0].Value) != 32 {
		return [32]byte{}, unauthenticated
	}
	return sha256.Sum256([]byte(cookies[0].Value)), nil
}

func lockedNativeOwnerSession(ctx context.Context, tx *sql.Tx, session [32]byte) (int64, int64, error) {
	// The first lookup identifies a lock target only; it grants no rights.
	// Provider login locks the user before deleting a predecessor session.
	// Pin the same user in its own statement before locking any session row;
	// a joined lock does not guarantee the executor will lock in that order.
	var actor int64
	err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE token_hash=$1`, session[:]).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, unauthenticated
	}
	if err != nil {
		return 0, 0, err
	}
	var password string
	err = tx.QueryRowContext(ctx, `SELECT password FROM users WHERE id=$1 AND active AND NOT staff
		AND NOT superuser AND password=$2 FOR SHARE`, actor, providerOnlyPassword).Scan(&password)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, unauthenticated
	}
	if err != nil {
		return 0, 0, err
	}
	// The original session may already have rotated while we waited. This
	// fresh exact recheck also excludes employee, classic and recovery sessions.
	var expires int64
	var saved []byte
	err = tx.QueryRowContext(ctx, `SELECT s.expires,s.password_digest
		FROM sessions s JOIN provider_identities i ON i.user_id=s.user_id
		WHERE s.token_hash=$1 AND s.user_id=$2 AND s.auth_method='provider'
		FOR SHARE OF s,i`, session[:], actor).Scan(&expires, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, unauthenticated
	}
	if err != nil {
		return 0, 0, err
	}
	expected := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(expected[:], saved) != 1 {
		return 0, 0, unauthenticated
	}
	return actor, expires, nil
}

func (a *app) previewNativeOwnerConsent(r *http.Request, store nativeOwnerConsentStore, intent string) (nativeOwnerPreview, error) {
	if !nativeOwnerOpaqueID(intent) {
		return nativeOwnerPreview{}, badInput
	}
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return nativeOwnerPreview{}, err
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nativeOwnerPreview{}, err
	}
	defer tx.Rollback()
	actor, expires, err := lockedNativeOwnerSession(ctx, tx, session)
	if err != nil {
		return nativeOwnerPreview{}, err
	}
	designation, err := store.lockNativeOwnerDesignation(ctx, tx, intent)
	if err != nil {
		return nativeOwnerPreview{}, nativeOwnerUnavailable(err)
	}
	target, valid := canonicalNativeOwnerTarget(designation.Target)
	now := time.Now().UTC()
	if !valid || target.Intent != intent || designation.Actor != actor || now.Unix() >= expires {
		return nativeOwnerPreview{}, nativeOwnerConsentUnavailable
	}
	designation.Target = target
	challenge := newProviderRandom()
	err = store.saveNativeOwnerPreview(ctx, tx, sha256.Sum256([]byte(challenge)),
		nativeOwnerBinding{Designation: designation, Session: session, Expires: now.Add(nativeOwnerConsentLifetime)})
	if err != nil {
		return nativeOwnerPreview{}, err
	}
	if err = tx.Commit(); err != nil {
		return nativeOwnerPreview{}, err
	}
	return nativeOwnerPreview{Challenge: challenge, Target: target}, nil
}

func (a *app) confirmNativeOwnerConsent(r *http.Request, store nativeOwnerConsentStore, confirmation nativeOwnerConfirmation) error {
	target, valid := canonicalNativeOwnerTarget(confirmation.Target)
	if !confirmation.Accept || !valid || !providerRandom(confirmation.Challenge) {
		return badInput
	}
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, expires, err := lockedNativeOwnerSession(ctx, tx, session)
	if err != nil {
		return err
	}
	designation, err := store.lockNativeOwnerDesignation(ctx, tx, target.Intent)
	if err != nil {
		return nativeOwnerUnavailable(err)
	}
	currentTarget, valid := canonicalNativeOwnerTarget(designation.Target)
	if !valid || designation.Actor != actor || !sameNativeOwnerTarget(currentTarget, target) {
		return nativeOwnerConsentUnavailable
	}
	key := sha256.Sum256([]byte(confirmation.Challenge))
	bound, err := store.lockNativeOwnerPreview(ctx, tx, key)
	if err != nil {
		return nativeOwnerUnavailable(err)
	}
	// Use fresh time after authorization, designation, and preview locks.
	now := time.Now().UTC()
	if actor != bound.Designation.Actor || bound.Session != session || !sameNativeOwnerTarget(bound.Designation.Target, target) ||
		now.Unix() >= expires || !now.Before(bound.Expires) {
		return nativeOwnerConsentUnavailable
	}
	if err = store.consumeNativeOwnerPreview(ctx, tx, key, now); err != nil {
		return nativeOwnerUnavailable(err)
	}
	return tx.Commit()
}
