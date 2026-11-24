package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
)

type nativeSignupPlan struct {
	Organization string
	Branches     []nativeInstallBranch
}

// Protected, unregistered writer. The designation and recovery approval
// must already exist. The display contains no legal or financial identity.
func (a *app) recordNativeSignupPlan(r *http.Request, expected nativeOwnerTarget, display nativeSignupPlan) error {
	target, valid := canonicalNativeOwnerTarget(expected)
	if !valid {
		return badInput
	}
	branches, valid := canonicalNativeOwnerDisplay(target, display.Organization, display.Branches)
	if !valid {
		return badInput
	}
	raw, err := json.Marshal(branches)
	if err != nil {
		return err
	}
	return a.nativeOwnerRecoveryTransaction(r, func(ctx context.Context, tx *sql.Tx, operator, sessionExpiry int64) error {
		// This lookup identifies a lock target only. Pressing consent takes
		// a shared actor lock; authoring any competing plan takes a
		// conflicting actor lock before the same intent lock. No new plan
		// can appear between single-plan selection and the consent commit.
		var actor, pinned int64
		if err := tx.QueryRowContext(ctx, `SELECT claimant_id FROM gomar.native_owner_designations
			WHERE intent=$1 AND revision=$2`, target.Intent, target.Revision).Scan(&actor); err != nil {
			return err
		}
		if actor == operator {
			return nativeOwnerConsentUnavailable
		}
		if err := tx.QueryRowContext(ctx, `SELECT u.id FROM gomar.users u
			WHERE u.id=$1 AND u.active AND NOT u.staff AND NOT u.superuser AND u.password=$2
			  AND EXISTS (SELECT 1 FROM gomar.provider_identities p WHERE p.user_id=u.id)
			FOR NO KEY UPDATE`, actor, providerOnlyPassword).Scan(&pinned); err != nil {
			return err
		}
		d, err := lockCurrentNativeOwnerDesignation(ctx, tx, target.Intent)
		if err != nil {
			return err
		}
		now := nativeOwnerNow()
		if d.Actor != pinned || !sameNativeOwnerTarget(d.Target, target) || !d.approvedAt(now) || now.Unix() >= sessionExpiry {
			return nativeOwnerConsentUnavailable
		}
		var one int
		return tx.QueryRowContext(ctx, `INSERT INTO gomar.native_signup_plans
			(intent,revision,approval_ref,authored_by,authored_at,organization,branch_display)
			SELECT $1,$2,$3,$4,$5,$6,$7
			WHERE NOT EXISTS (SELECT 1 FROM gomar.native_owner_receipts WHERE intent=$1)
			ON CONFLICT (intent,revision) DO NOTHING RETURNING 1`,
			target.Intent, target.Revision, d.ApprovalRef.String, operator, now, display.Organization, raw).Scan(&one)
	})
}

// The browser never supplies the selector. The verified session holds the
// claimant lock before enumeration, and only one currently eligible plan
// can be selected. Old plans are history, not fallback candidates.
func loadNativeSignupPlan(ctx context.Context, tx *sql.Tx, browser nativeOwnerBrowserState, audience string) (nativeSignupPlan, nativeOwnerDesignation, error) {
	var plan nativeSignupPlan
	var d nativeOwnerDesignation
	now := nativeOwnerNow()
	if now.Unix() >= browser.Expires {
		return plan, d, unauthenticated
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.intent,p.revision
		FROM gomar.native_owner_designations d
		JOIN gomar.native_signup_plans p ON (p.intent,p.revision)=(d.intent,d.revision)
		JOIN gomar.native_owner_intents i ON i.intent=d.intent
		JOIN gomar.native_owner_approvals a ON (a.intent,a.revision,a.approval_ref)=(p.intent,p.revision,p.approval_ref)
		WHERE d.claimant_id=$1 AND i.audience=$2 AND d.expires_at>$3 AND a.expires_at>$3
		  AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_designations newer WHERE newer.intent=d.intent AND newer.revision>d.revision)
		  AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_approval_revocations v WHERE v.approval_ref=p.approval_ref)
		  AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_receipts r WHERE r.intent=d.intent)
		LIMIT 2`, browser.Actor, audience, now)
	if err != nil {
		return plan, d, err
	}
	defer rows.Close()
	var intent string
	var revision int64
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return plan, d, err
		}
		return plan, d, nativeOwnerConsentUnavailable
	}
	if err = rows.Scan(&intent, &revision); err != nil {
		return plan, d, err
	}
	if rows.Next() {
		return plan, d, nativeOwnerConsentUnavailable
	}
	if err = rows.Err(); err != nil {
		return plan, d, err
	}
	// Exhausted query rows before locking. Revocation, supersession,
	// expiration, or consumption may have won the intent lock meanwhile.
	d, err = lockCurrentNativeOwnerDesignation(ctx, tx, intent)
	if err != nil {
		return plan, d, nativeOwnerUnavailable(err)
	}
	now = nativeOwnerNow()
	if d.Actor != browser.Actor || d.Target.Audience != audience || d.Target.Revision != revision ||
		!d.approvedAt(now) || now.Unix() >= browser.Expires {
		return plan, d, nativeOwnerConsentUnavailable
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT p.organization,p.branch_display FROM gomar.native_signup_plans p
		WHERE p.intent=$1 AND p.revision=$2 AND p.approval_ref=$3
		  AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_receipts r WHERE r.intent=p.intent)`,
		intent, revision, d.ApprovalRef.String).Scan(&plan.Organization, &raw)
	if err != nil {
		return plan, d, nativeOwnerUnavailable(err)
	}
	var branches []nativeInstallBranch
	if json.Unmarshal(raw, &branches) != nil {
		return nativeSignupPlan{}, d, nativeOwnerConsentUnavailable
	}
	var valid bool
	if plan.Branches, valid = canonicalNativeOwnerDisplay(d.Target, plan.Organization, branches); !valid {
		return nativeSignupPlan{}, d, nativeOwnerConsentUnavailable
	}
	return plan, d, nil
}
