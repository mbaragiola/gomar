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

const (
	nativeOwnerDesignationLifetime = 24 * time.Hour
	nativeOwnerApprovalLifetime    = 30 * time.Minute
	nativeOwnerConsentLifetime     = 5 * time.Minute
)

var nativeOwnerConsentUnavailable = problem{404, "No hay una solicitud de autorización vigente para esta cuenta y sesión."}

// This contract has no registered route or caller in the running app.
// These are opaque scope identifiers, not billing records or local permissions.
type nativeOwnerTarget struct {
	Intent     string   `json:"intent"`
	Business   string   `json:"business"`
	Audience   string   `json:"audience"`
	RequestRef string   `json:"request_ref"`
	Outlets    []string `json:"outlets"`
	Revision   int64    `json:"revision"`
}

type nativeOwnerDesignation struct {
	Actor           int64
	Target          nativeOwnerTarget
	Expires         time.Time
	ApprovalRef     sql.NullString
	ApprovalExpires sql.NullTime
	Revoked         bool
}

func (d nativeOwnerDesignation) approvedAt(now time.Time) bool {
	return d.ApprovalRef.Valid && d.ApprovalExpires.Valid && !d.Revoked &&
		now.Before(d.Expires) && now.Before(d.ApprovalExpires.Time)
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

// A receipt reference is an identifier, not a credential. This projection
// intentionally omits actor IDs and browser/session/provider credentials.
type nativeOwnerReceipt struct {
	Ref         string            `json:"receipt_ref"`
	Target      nativeOwnerTarget `json:"target"`
	ApprovalRef string            `json:"approval_ref"`
	AcceptedAt  time.Time         `json:"accepted_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
}

type nativeOwnerReceiptStatus struct {
	Receipt   nativeOwnerReceipt `json:"receipt"`
	CheckedAt time.Time          `json:"checked_at"`
	Current   bool               `json:"current"`
}

func nativeOwnerNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func nativeOwnerDeadline(first time.Time, limits ...time.Time) time.Time {
	for _, limit := range limits {
		if limit.Before(first) {
			first = limit
		}
	}
	return first
}

func nativeOwnerRef(value, prefix string) bool {
	return len(value) == len(prefix)+43 && value[:len(prefix)] == prefix && providerRandom(value[len(prefix):])
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
	if !nativeOwnerOpaqueID(target.Intent) || !nativeOwnerOpaqueID(target.Business) || !nativeOwnerOpaqueID(target.Audience) ||
		!nativeOwnerOpaqueID(target.RequestRef) || target.Revision <= 0 ||
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
	return a.Intent == b.Intent && a.Business == b.Business && a.Audience == b.Audience &&
		a.RequestRef == b.RequestRef && a.Revision == b.Revision && slices.Equal(a.Outlets, b.Outlets)
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

// All browser callers pin the ordinary actor and its exact session before
// any intent. An open transaction is the boundary for scope and consent.
type nativeOwnerBrowserState struct {
	Actor, Expires int64
	Session        [32]byte
}

func (a *app) beginNativeOwnerBrowser(ctx context.Context, session [32]byte) (*sql.Tx, nativeOwnerBrowserState, error) {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, nativeOwnerBrowserState{}, err
	}
	actor, expires, err := lockedNativeOwnerSession(ctx, tx, session)
	if err != nil {
		tx.Rollback()
		return nil, nativeOwnerBrowserState{}, err
	}
	return tx, nativeOwnerBrowserState{Actor: actor, Expires: expires, Session: session}, nil
}

// The browser adapter also binds the precise reviewed labels. It has a
// separate hash domain so the same challenge cannot be used on a path
// which did not verify that display. No raw token or label storage here.
type nativeOwnerReviewBinding struct {
	Actor   int64
	Target  nativeOwnerTarget
	Display [32]byte
}

func nativeOwnerChallengeHash(challenge string, review *nativeOwnerReviewBinding) [32]byte {
	if review == nil {
		return sha256.Sum256([]byte(challenge))
	}
	h := sha256.New()
	h.Write([]byte("gomar-native-signup-review-v1\x00"))
	h.Write(review.Display[:])
	h.Write([]byte(challenge))
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

func (a *app) previewNativeOwnerConsent(r *http.Request, intent, audience string) (nativeOwnerPreview, error) {
	return a.previewBoundNativeOwnerConsent(r, intent, audience, nil)
}

// Take the same locks in this order in preview, consent and reading: actor,
// exact current session, intent/current designation, then preview if needed.
func (a *app) previewBoundNativeOwnerConsent(r *http.Request, intent, audience string, review *nativeOwnerReviewBinding) (nativeOwnerPreview, error) {
	if !nativeOwnerOpaqueID(intent) || !nativeOwnerOpaqueID(audience) {
		return nativeOwnerPreview{}, badInput
	}
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return nativeOwnerPreview{}, err
	}
	ctx := r.Context()
	tx, browser, err := a.beginNativeOwnerBrowser(ctx, session)
	if err != nil {
		return nativeOwnerPreview{}, err
	}
	defer tx.Rollback()
	d, err := lockCurrentNativeOwnerDesignation(ctx, tx, intent)
	if err != nil {
		return nativeOwnerPreview{}, nativeOwnerUnavailable(err)
	}
	preview, err := writeNativeOwnerPreview(ctx, tx, browser, d, audience, review)
	if err != nil {
		return nativeOwnerPreview{}, err
	}
	if err = tx.Commit(); err != nil {
		return nativeOwnerPreview{}, err
	}
	return preview, nil
}

// Requires the existing actor/session/intent locks in this transaction.
func writeNativeOwnerPreview(ctx context.Context, tx *sql.Tx, browser nativeOwnerBrowserState, d nativeOwnerDesignation, audience string, review *nativeOwnerReviewBinding) (nativeOwnerPreview, error) {
	now := nativeOwnerNow()
	if d.Actor != browser.Actor || d.Target.Audience != audience || now.Unix() >= browser.Expires || !d.approvedAt(now) ||
		(review != nil && (browser.Actor != review.Actor || !sameNativeOwnerTarget(d.Target, review.Target))) {
		return nativeOwnerPreview{}, nativeOwnerConsentUnavailable
	}
	challenge := newProviderRandom()
	key := nativeOwnerChallengeHash(challenge, review)
	expires := nativeOwnerDeadline(now.Add(nativeOwnerConsentLifetime), d.Expires, d.ApprovalExpires.Time, time.Unix(browser.Expires, 0))
	var inserted int
	err := tx.QueryRowContext(ctx, `INSERT INTO gomar.native_owner_previews
		(challenge_hash,session_hash,intent,revision,approval_ref,previewed_at,expires_at)
		SELECT $1,$2,$3,$4,$5,$6,$7 WHERE NOT EXISTS (
			SELECT 1 FROM gomar.native_owner_receipts WHERE intent=$3)
		RETURNING 1`, key[:], browser.Session[:], d.Target.Intent, d.Target.Revision, d.ApprovalRef.String, now, expires).Scan(&inserted)
	if err != nil {
		return nativeOwnerPreview{}, nativeOwnerUnavailable(err)
	}
	return nativeOwnerPreview{Challenge: challenge, Target: d.Target}, nil
}

func (a *app) confirmNativeOwnerConsent(r *http.Request, c nativeOwnerConfirmation) (nativeOwnerReceipt, error) {
	return a.confirmBoundNativeOwnerConsent(r, c, nil)
}

func (a *app) confirmBoundNativeOwnerConsent(r *http.Request, c nativeOwnerConfirmation, review *nativeOwnerReviewBinding) (nativeOwnerReceipt, error) {
	target, valid := canonicalNativeOwnerTarget(c.Target)
	if !c.Accept || !valid || !providerRandom(c.Challenge) || (review != nil && !sameNativeOwnerTarget(target, review.Target)) {
		return nativeOwnerReceipt{}, badInput
	}
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return nativeOwnerReceipt{}, err
	}
	ctx := r.Context()
	tx, browser, err := a.beginNativeOwnerBrowser(ctx, session)
	if err != nil {
		return nativeOwnerReceipt{}, err
	}
	defer tx.Rollback()
	d, err := lockCurrentNativeOwnerDesignation(ctx, tx, target.Intent)
	if err != nil {
		return nativeOwnerReceipt{}, nativeOwnerUnavailable(err)
	}
	receipt, err := writeNativeOwnerConfirmation(ctx, tx, browser, d, target, c.Challenge, review)
	if err != nil {
		return nativeOwnerReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return nativeOwnerReceipt{}, err
	}
	return receipt, nil
}

// Requires the existing actor/session/intent locks, and a caller which
// has validated explicit consent and the challenge's canonical encoding.
func writeNativeOwnerConfirmation(ctx context.Context, tx *sql.Tx, browser nativeOwnerBrowserState, d nativeOwnerDesignation,
	target nativeOwnerTarget, challenge string, review *nativeOwnerReviewBinding) (nativeOwnerReceipt, error) {
	if d.Actor != browser.Actor || !sameNativeOwnerTarget(d.Target, target) || !d.ApprovalRef.Valid ||
		(review != nil && (review.Actor != browser.Actor || !sameNativeOwnerTarget(target, review.Target))) {
		return nativeOwnerReceipt{}, nativeOwnerConsentUnavailable
	}
	key := nativeOwnerChallengeHash(challenge, review)
	var boundSession []byte
	var previewExpiry time.Time
	err := tx.QueryRowContext(ctx, `SELECT session_hash,expires_at FROM gomar.native_owner_previews
		WHERE challenge_hash=$1 AND intent=$2 AND revision=$3 AND approval_ref=$4 FOR UPDATE`,
		key[:], target.Intent, target.Revision, d.ApprovalRef.String).Scan(&boundSession, &previewExpiry)
	if err != nil {
		return nativeOwnerReceipt{}, nativeOwnerUnavailable(err)
	}
	now := nativeOwnerNow()
	if subtle.ConstantTimeCompare(boundSession, browser.Session[:]) != 1 || now.Unix() >= browser.Expires ||
		!now.Before(previewExpiry) || !d.approvedAt(now) {
		return nativeOwnerReceipt{}, nativeOwnerConsentUnavailable
	}
	ref := "accept-" + newProviderRandom()
	expires := nativeOwnerDeadline(now.Add(nativeOwnerConsentLifetime), previewExpiry, d.Expires, d.ApprovalExpires.Time, time.Unix(browser.Expires, 0))
	var accepted int
	var display any // SQL NULL for the existing unbound internal path.
	if review != nil {
		display = review.Display[:]
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO gomar.native_owner_receipts
		(receipt_ref,challenge_hash,intent,revision,approval_ref,accepted_at,expires_at,display_digest)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (intent) DO NOTHING RETURNING 1`,
		ref, key[:], target.Intent, target.Revision, d.ApprovalRef.String, now, expires, display).Scan(&accepted)
	if err != nil {
		return nativeOwnerReceipt{}, nativeOwnerUnavailable(err)
	}
	return nativeOwnerReceipt{Ref: ref, Target: target, ApprovalRef: d.ApprovalRef.String, AcceptedAt: now, ExpiresAt: expires}, nil
}

// This exact lookup is protected separately from approval. A historical
// receipt remains visible to its exact claimant but is never current merely
// because it exists or because its opaque reference was presented.
func (a *app) readNativeOwnerReceipt(r *http.Request, expected nativeOwnerTarget, ref string) (nativeOwnerReceiptStatus, error) {
	target, valid := canonicalNativeOwnerTarget(expected)
	if !valid || !nativeOwnerRef(ref, "accept-") {
		return nativeOwnerReceiptStatus{}, badInput
	}
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return nativeOwnerReceiptStatus{}, err
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nativeOwnerReceiptStatus{}, err
	}
	defer tx.Rollback()
	actor, sessionExpiry, err := lockedNativeOwnerSession(ctx, tx, session)
	if err != nil {
		return nativeOwnerReceiptStatus{}, err
	}
	current, err := lockCurrentNativeOwnerDesignation(ctx, tx, target.Intent)
	if err != nil {
		return nativeOwnerReceiptStatus{}, nativeOwnerUnavailable(err)
	}
	historical := current
	if current.Target.Revision != target.Revision {
		historical, err = loadNativeOwnerDesignation(ctx, tx, target.Intent, target.Revision)
		if err != nil {
			return nativeOwnerReceiptStatus{}, nativeOwnerUnavailable(err)
		}
	}
	if historical.Actor != actor || !sameNativeOwnerTarget(historical.Target, target) {
		return nativeOwnerReceiptStatus{}, nativeOwnerConsentUnavailable
	}
	result := nativeOwnerReceiptStatus{Receipt: nativeOwnerReceipt{Ref: ref, Target: historical.Target}}
	var original []byte
	err = tx.QueryRowContext(ctx, `SELECT r.approval_ref,r.accepted_at,r.expires_at,p.session_hash
		FROM gomar.native_owner_receipts r JOIN gomar.native_owner_previews p ON p.challenge_hash=r.challenge_hash
		WHERE r.receipt_ref=$1 AND r.intent=$2 AND r.revision=$3`, ref, target.Intent, target.Revision).
		Scan(&result.Receipt.ApprovalRef, &result.Receipt.AcceptedAt, &result.Receipt.ExpiresAt, &original)
	if err != nil {
		return nativeOwnerReceiptStatus{}, nativeOwnerUnavailable(err)
	}
	now := nativeOwnerNow()
	if now.Unix() >= sessionExpiry {
		return nativeOwnerReceiptStatus{}, nativeOwnerConsentUnavailable
	}
	result.Receipt.AcceptedAt, result.Receipt.ExpiresAt = result.Receipt.AcceptedAt.UTC(), result.Receipt.ExpiresAt.UTC()
	result.CheckedAt = now
	result.Current = sameNativeOwnerTarget(current.Target, target) && current.ApprovalRef.String == result.Receipt.ApprovalRef &&
		current.approvedAt(now) && now.Before(result.Receipt.ExpiresAt) && subtle.ConstantTimeCompare(original, session[:]) == 1
	if err = tx.Commit(); err != nil {
		return nativeOwnerReceiptStatus{}, err
	}
	return result, nil
}
