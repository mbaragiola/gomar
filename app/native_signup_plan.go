package main

import (
	"context"
	"crypto/subtle"
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

type nativeSignupState string

const (
	nativeSignupReviewable nativeSignupState = "review"
	nativeSignupConfirmed  nativeSignupState = "confirmed"
	nativeSignupPrepared   nativeSignupState = "prepared"
)

type nativeSignupSelection struct {
	Plan        nativeSignupPlan
	Designation nativeOwnerDesignation
	State       nativeSignupState
}

// All paths use the same ambiguity check. The browser never supplies the
// selector; an eligible exact plan or a receipt bound to this original
// session is a candidate, and a history plus another candidate is denied.
func loadNativeSignupSelection(ctx context.Context, tx *sql.Tx, browser nativeOwnerBrowserState, audience string) (nativeSignupSelection, error) {
	var result nativeSignupSelection
	now := nativeOwnerNow()
	if now.Unix() >= browser.Expires {
		return result, unauthenticated
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.intent,p.revision
		FROM gomar.native_owner_designations d
		JOIN gomar.native_signup_plans p ON (p.intent,p.revision)=(d.intent,d.revision)
		JOIN gomar.native_owner_intents i ON i.intent=d.intent
		JOIN gomar.native_owner_approvals a ON (a.intent,a.revision,a.approval_ref)=(p.intent,p.revision,p.approval_ref)
		LEFT JOIN gomar.native_owner_receipts r ON r.intent=d.intent
		LEFT JOIN gomar.native_owner_previews previous ON previous.challenge_hash=r.challenge_hash
		WHERE d.claimant_id=$1 AND i.audience=$2 AND (
		  (r.intent IS NULL AND d.expires_at>$3 AND a.expires_at>$3
		   AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_designations newer WHERE newer.intent=d.intent AND newer.revision>d.revision)
		   AND NOT EXISTS (SELECT 1 FROM gomar.native_owner_approval_revocations v WHERE v.approval_ref=p.approval_ref))
		  OR (r.revision=p.revision AND r.approval_ref=p.approval_ref AND r.display_digest IS NOT NULL
		   AND previous.session_hash=$4))
		LIMIT 2`, browser.Actor, audience, now, browser.Session[:])
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var intent string
	var revision int64
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return result, err
		}
		return result, nativeOwnerConsentUnavailable
	}
	if err = rows.Scan(&intent, &revision); err != nil {
		return result, err
	}
	if rows.Next() {
		return result, nativeOwnerConsentUnavailable
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	// Exhausted rows before locking. Consume/revoke/supersede may have won
	// this intent already. Inspect authoritative rows again under its lock.
	if _, _, err = lockNativeOwnerIntent(ctx, tx, intent); err != nil {
		return result, nativeOwnerUnavailable(err)
	}
	var raw, digest, original []byte
	var hasReceipt, sameReceipt, prepared bool
	err = tx.QueryRowContext(ctx, `SELECT p.organization,p.branch_display,r.intent IS NOT NULL,
		COALESCE(r.revision=p.revision AND r.approval_ref=p.approval_ref,false),r.display_digest,previous.session_hash,
		EXISTS (SELECT 1 FROM gomar.native_owner_pending_installs install WHERE install.intent=p.intent
		 AND install.consent_ref=r.receipt_ref AND install.planned_owner_id=$3)
		FROM gomar.native_signup_plans p
		LEFT JOIN gomar.native_owner_receipts r ON r.intent=p.intent
		LEFT JOIN gomar.native_owner_previews previous ON previous.challenge_hash=r.challenge_hash
		WHERE p.intent=$1 AND p.revision=$2`, intent, revision, browser.Actor).
		Scan(&result.Plan.Organization, &raw, &hasReceipt, &sameReceipt, &digest, &original, &prepared)
	if err != nil {
		return nativeSignupSelection{}, nativeOwnerUnavailable(err)
	}
	loadRevision := int64(0) // Live review uses only the current designation.
	if hasReceipt {
		loadRevision = revision // History never gains current authorization.
	}
	d, err := loadNativeOwnerDesignation(ctx, tx, intent, loadRevision)
	if err != nil {
		return nativeSignupSelection{}, nativeOwnerUnavailable(err)
	}
	now = nativeOwnerNow()
	if d.Actor != browser.Actor || d.Target.Audience != audience || d.Target.Revision != revision ||
		now.Unix() >= browser.Expires {
		return nativeSignupSelection{}, nativeOwnerConsentUnavailable
	}
	var branches []nativeInstallBranch
	if json.Unmarshal(raw, &branches) != nil {
		return nativeSignupSelection{}, nativeOwnerConsentUnavailable
	}
	var valid bool
	if result.Plan.Branches, valid = canonicalNativeOwnerDisplay(d.Target, result.Plan.Organization, branches); !valid {
		return nativeSignupSelection{}, nativeOwnerConsentUnavailable
	}
	result.Designation = d
	if hasReceipt {
		expected := nativeOwnerDisplayDigest(browser.Actor, d.Target, result.Plan.Organization, result.Plan.Branches)
		if !sameReceipt || subtle.ConstantTimeCompare(digest, expected[:]) != 1 ||
			subtle.ConstantTimeCompare(original, browser.Session[:]) != 1 {
			return nativeSignupSelection{}, nativeOwnerConsentUnavailable
		}
		result.State = nativeSignupConfirmed
		if prepared {
			result.State = nativeSignupPrepared
		}
		return result, nil
	}
	if !d.approvedAt(now) {
		return nativeSignupSelection{}, nativeOwnerConsentUnavailable
	}
	result.State = nativeSignupReviewable
	return result, nil
}
