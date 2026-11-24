package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// The immutable intent row is the transaction gate for all versions,
// approvals, revocations, previews and receipts for this one intent.
func lockNativeOwnerIntent(ctx context.Context, tx *sql.Tx, intent string) (business, audience string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT business,audience FROM gomar.native_owner_intents
		WHERE intent=$1 FOR UPDATE`, intent).Scan(&business, &audience)
	return
}

func lockCurrentNativeOwnerDesignation(ctx context.Context, tx *sql.Tx, intent string) (nativeOwnerDesignation, error) {
	if _, _, err := lockNativeOwnerIntent(ctx, tx, intent); err != nil {
		return nativeOwnerDesignation{}, err
	}
	return loadNativeOwnerDesignation(ctx, tx, intent, 0)
}

// The caller must already own the intent row lock. Zero selects the newest
// designation, including a newer one which has no approval.
func loadNativeOwnerDesignation(ctx context.Context, tx *sql.Tx, intent string, revision int64) (nativeOwnerDesignation, error) {
	var d nativeOwnerDesignation
	d.Target.Intent = intent
	var outlets []byte
	err := tx.QueryRowContext(ctx, `SELECT i.business,i.audience,d.request_ref,d.revision,array_to_json(d.outlets),d.claimant_id,
		d.expires_at,a.approval_ref,a.expires_at,(v.approval_ref IS NOT NULL)
		FROM gomar.native_owner_intents i JOIN gomar.native_owner_designations d ON d.intent=i.intent
		LEFT JOIN gomar.native_owner_approvals a ON a.intent=d.intent AND a.revision=d.revision
		LEFT JOIN gomar.native_owner_approval_revocations v ON v.approval_ref=a.approval_ref
		WHERE d.intent=$1 AND ($2::bigint=0 OR d.revision=$2) ORDER BY d.revision DESC LIMIT 1`, intent, revision).
		Scan(&d.Target.Business, &d.Target.Audience, &d.Target.RequestRef, &d.Target.Revision, &outlets,
			&d.Actor, &d.Expires, &d.ApprovalRef, &d.ApprovalExpires, &d.Revoked)
	if err == nil {
		err = json.Unmarshal(outlets, &d.Target.Outlets)
	}
	return d, err
}

func (a *app) nativeOwnerRecoverySession(r *http.Request) ([32]byte, error) {
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		return [32]byte{}, badInput
	}
	origin := r.Header.Values("Origin")
	// Protected ingress and realm cookies are both necessary. Do not use
	// currentAdminUser's compatible-mode fallback to normal app sessions.
	if !a.adminRecovery || !isPrivateHTTPS(r) || r.Host != a.host || !strings.HasPrefix(r.URL.Path, "/admin/") ||
		len(origin) != 1 || origin[0] != "https://"+a.host || len(r.Header.Values("X-CSRFToken")) != 1 ||
		len(r.CookiesNamed(adminRecoveryCSRF)) != 1 || !a.validAdminCSRF(r) {
		return [32]byte{}, badCSRF
	}
	cookies := r.CookiesNamed(adminRecoveryCookie)
	if len(cookies) != 1 || len(cookies[0].Value) != 32 {
		return [32]byte{}, unauthenticated
	}
	return sha256.Sum256([]byte(cookies[0].Value)), nil
}

func lockedNativeOwnerOperator(ctx context.Context, tx *sql.Tx, session [32]byte) (int64, int64, error) {
	var operator, expires int64
	err := tx.QueryRowContext(ctx, `SELECT user_id FROM gomar.admin_recovery_sessions WHERE token_hash=$1`, session[:]).Scan(&operator)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, unauthenticated
	}
	if err != nil {
		return 0, 0, err
	}
	var password string
	err = tx.QueryRowContext(ctx, `SELECT password FROM gomar.users WHERE id=$1 AND active AND staff AND superuser
		AND password<>$2 FOR SHARE`, operator, providerOnlyPassword).Scan(&password)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, unauthenticated
	}
	if err != nil {
		return 0, 0, err
	}
	var saved []byte
	err = tx.QueryRowContext(ctx, `SELECT password_digest,expires FROM gomar.admin_recovery_sessions
		WHERE token_hash=$1 AND user_id=$2 FOR SHARE`, session[:], operator).Scan(&saved, &expires)
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
	return operator, expires, nil
}

func (a *app) nativeOwnerRecoveryTransaction(r *http.Request, update func(context.Context, *sql.Tx, int64, int64) error) error {
	session, err := a.nativeOwnerRecoverySession(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operator, expires, err := lockedNativeOwnerOperator(ctx, tx, session)
	if err != nil {
		return err
	}
	if err = update(ctx, tx, operator, expires); err != nil {
		return nativeOwnerUnavailable(err)
	}
	return tx.Commit()
}

// The claimant mapping comes from a trusted native caller; there is no
// public caller or input schema that can designate a browser-chosen owner.
func (a *app) designateNativeOwner(r *http.Request, expected nativeOwnerTarget, claimant int64) error {
	target, ok := canonicalNativeOwnerTarget(expected)
	if !ok || claimant <= 0 {
		return badInput
	}
	return a.nativeOwnerRecoveryTransaction(r, func(ctx context.Context, tx *sql.Tx, operator, sessionExpiry int64) error {
		// Pin the distinct claimant before the intent so the FK never has
		// to acquire that user lock after we acquired the intent.
		var owner int64
		if err := tx.QueryRowContext(ctx, `SELECT u.id FROM gomar.users u JOIN gomar.provider_identities i ON i.user_id=u.id
			WHERE u.id=$1 AND u.active AND NOT u.staff AND NOT u.superuser AND u.password=$2 FOR SHARE OF u`,
			claimant, providerOnlyPassword).Scan(&owner); err != nil {
			return err
		}
		if owner == operator {
			return nativeOwnerConsentUnavailable
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO gomar.native_owner_intents(intent,business,audience)
			VALUES($1,$2,$3) ON CONFLICT (intent) DO NOTHING`, target.Intent, target.Business, target.Audience); err != nil {
			return err
		}
		business, audience, err := lockNativeOwnerIntent(ctx, tx, target.Intent)
		if err != nil {
			return err
		}
		var highest int64
		var accepted bool
		if err = tx.QueryRowContext(ctx, `SELECT
			(SELECT COALESCE(MAX(revision),0) FROM gomar.native_owner_designations WHERE intent=$1),
			EXISTS (SELECT 1 FROM gomar.native_owner_receipts WHERE intent=$1)`, target.Intent).Scan(&highest, &accepted); err != nil {
			return err
		}
		now := nativeOwnerNow()
		if business != target.Business || audience != target.Audience || target.Revision <= highest || accepted || now.Unix() >= sessionExpiry {
			return nativeOwnerConsentUnavailable
		}
		outlets, err := json.Marshal(target.Outlets)
		if err != nil {
			return err
		}
		var one int
		return tx.QueryRowContext(ctx, `INSERT INTO gomar.native_owner_designations
			(intent,revision,request_ref,claimant_id,designated_by,outlets,designated_at,expires_at)
			VALUES($1,$2,$3,$4,$5,ARRAY(SELECT jsonb_array_elements_text($6::jsonb)),$7,$8)
			ON CONFLICT (request_ref) DO NOTHING RETURNING 1`,
			target.Intent, target.Revision, target.RequestRef, owner, operator, outlets, now, now.Add(nativeOwnerDesignationLifetime)).Scan(&one)
	})
}

func (a *app) approveNativeOwnerDesignation(r *http.Request, expected nativeOwnerTarget) (string, error) {
	target, ok := canonicalNativeOwnerTarget(expected)
	if !ok {
		return "", badInput
	}
	ref := "approval-" + newProviderRandom()
	err := a.nativeOwnerRecoveryTransaction(r, func(ctx context.Context, tx *sql.Tx, operator, sessionExpiry int64) error {
		d, err := lockCurrentNativeOwnerDesignation(ctx, tx, target.Intent)
		if err != nil {
			return err
		}
		now := nativeOwnerNow()
		if d.Actor == operator || !sameNativeOwnerTarget(d.Target, target) || d.ApprovalRef.Valid ||
			!now.Before(d.Expires) || now.Unix() >= sessionExpiry {
			return nativeOwnerConsentUnavailable
		}
		var one int
		return tx.QueryRowContext(ctx, `INSERT INTO gomar.native_owner_approvals
			(intent,revision,approval_ref,operator_id,approved_at,expires_at)
			SELECT $1,$2,$3,$4,$5,$6 WHERE NOT EXISTS (SELECT 1 FROM gomar.native_owner_receipts WHERE intent=$1)
			RETURNING 1`, target.Intent, target.Revision,
			ref, operator, now, nativeOwnerDeadline(now.Add(nativeOwnerApprovalLifetime), d.Expires)).Scan(&one)
	})
	if err != nil {
		return "", err
	}
	return ref, nil
}

func (a *app) revokeNativeOwnerApproval(r *http.Request, expected nativeOwnerTarget, ref string) error {
	target, ok := canonicalNativeOwnerTarget(expected)
	if !ok || !nativeOwnerRef(ref, "approval-") {
		return badInput
	}
	return a.nativeOwnerRecoveryTransaction(r, func(ctx context.Context, tx *sql.Tx, operator, sessionExpiry int64) error {
		if _, _, err := lockNativeOwnerIntent(ctx, tx, target.Intent); err != nil {
			return err
		}
		d, err := loadNativeOwnerDesignation(ctx, tx, target.Intent, target.Revision)
		if err != nil {
			return err
		}
		now := nativeOwnerNow()
		if d.Actor == operator || !sameNativeOwnerTarget(d.Target, target) || d.ApprovalRef.String != ref ||
			d.Revoked || now.Unix() >= sessionExpiry {
			return nativeOwnerConsentUnavailable
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO gomar.native_owner_approval_revocations
			(approval_ref,operator_id,revoked_at) VALUES($1,$2,$3)`, ref, operator, now)
		return err
	})
}
