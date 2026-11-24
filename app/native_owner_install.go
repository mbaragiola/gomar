package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	nativeInstallPurpose = "first-install-observation"
	nativeInstallWindow  = 5 * time.Second
)

type nativeInstallBranch struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

type nativeInstallDraft struct {
	Target              nativeOwnerTarget
	ConsentRef          string
	DisplayOrganization string
	LegalName, CUIT     string
	Initiator, Claimant string
	Branches            []nativeInstallBranch
}

type nativeInstallPeer struct{ Issuer, Recipient string }

type nativeInstallRequest struct {
	Peer                    nativeInstallPeer
	Purpose                 string
	Schema                  int
	Target                  nativeOwnerTarget
	Initiator, Claimant     string
	ConsentRef, ApprovalRef string
	Nonce                   string
}

// A trusted adapter must authenticate the peer and correlate the exact
// request before constructing this value. Billing's minimal observation
// alone does not authenticate or carry the binding. There is no such adapter
// in production, and this type is not a wire contract or browser input.
type nativeInstallObservation struct {
	Binding              nativeInstallRequest
	CheckedAt, ExpiresAt time.Time
}

type nativeInstallVerifier func(context.Context, nativeInstallRequest) (nativeInstallObservation, error)

type nativeInstallReceipt struct {
	Ref        string    `json:"install_ref"`
	RecordedAt time.Time `json:"recorded_at"`
	Dormant    bool      `json:"dormant"`
}

type nativeInstallState struct {
	owner      int64
	draft      nativeInstallDraft
	consent    nativeOwnerReceipt
	validUntil time.Time
	history    nativeInstallReceipt
}

func nativeInstallName(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= 160 && !strings.ContainsFunc(value, unicode.IsControl)
}

func canonicalNativeInstallDraft(draft nativeInstallDraft) (nativeInstallDraft, bool) {
	target, valid := canonicalNativeOwnerTarget(draft.Target)
	cuit, err := normalizeCUIT(draft.CUIT)
	if !valid || !nativeOwnerRef(draft.ConsentRef, "accept-") || !nativeInstallName(draft.LegalName) ||
		err != nil || !nativeOwnerOpaqueID(draft.Initiator) || !nativeOwnerOpaqueID(draft.Claimant) ||
		draft.Initiator == draft.Claimant || len(draft.Branches) != len(target.Outlets) {
		return nativeInstallDraft{}, false
	}
	branches, valid := canonicalNativeOwnerDisplay(target, draft.DisplayOrganization, draft.Branches)
	if !valid {
		return nativeInstallDraft{}, false
	}
	draft.Target, draft.CUIT, draft.Branches = target, cuit, branches
	return draft, true
}

func loadNativeInstallConsent(ctx context.Context, tx *sql.Tx, target nativeOwnerTarget, ref string) (nativeOwnerReceipt, []byte, []byte, error) {
	receipt := nativeOwnerReceipt{Ref: ref, Target: target}
	var original, display []byte
	err := tx.QueryRowContext(ctx, `SELECT r.approval_ref,r.accepted_at,r.expires_at,p.session_hash,r.display_digest
		FROM gomar.native_owner_receipts r JOIN gomar.native_owner_previews p ON p.challenge_hash=r.challenge_hash
		WHERE r.receipt_ref=$1 AND r.intent=$2 AND r.revision=$3`, ref, target.Intent, target.Revision).
		Scan(&receipt.ApprovalRef, &receipt.AcceptedAt, &receipt.ExpiresAt, &original, &display)
	return receipt, original, display, err
}

// The recovery operator approves the private local draft through the existing
// isolated realm. The caller supplies a trusted opaque claimant/initiator
// mapping; no production caller or route is registered.
func (a *app) approveNativeInstallDraft(r *http.Request, expected nativeInstallDraft) error {
	draft, valid := canonicalNativeInstallDraft(expected)
	if !valid {
		return badInput
	}
	return a.nativeOwnerRecoveryTransaction(r, func(ctx context.Context, tx *sql.Tx, operator, sessionExpiry int64) error {
		d, err := lockCurrentNativeOwnerDesignation(ctx, tx, draft.Target.Intent)
		if err != nil {
			return err
		}
		if d.Actor == operator || !sameNativeOwnerTarget(d.Target, draft.Target) {
			return nativeOwnerConsentUnavailable
		}
		consent, _, display, err := loadNativeInstallConsent(ctx, tx, draft.Target, draft.ConsentRef)
		if err != nil {
			return err
		}
		now := nativeOwnerNow()
		if now.Unix() >= sessionExpiry || !d.approvedAt(now) || d.ApprovalRef.String != consent.ApprovalRef ||
			!now.Before(consent.ExpiresAt) || !matchesNativeOwnerDisplay(display, d.Actor, draft) {
			return nativeOwnerConsentUnavailable
		}
		branches, err := json.Marshal(draft.Branches)
		if err != nil {
			return err
		}
		var one int
		return tx.QueryRowContext(ctx, `INSERT INTO gomar.native_owner_install_drafts
			(intent,consent_ref,approved_by,approved_at,legal_name,cuit,initiator_ref,claimant_ref,branch_plan,display_organization)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10 WHERE NOT EXISTS (SELECT 1 FROM gomar.entities WHERE cuit=$6)
			ON CONFLICT (intent) DO NOTHING RETURNING 1`,
			draft.Target.Intent, draft.ConsentRef, operator, now, draft.LegalName, draft.CUIT,
			draft.Initiator, draft.Claimant, branches, draft.DisplayOrganization).Scan(&one)
	})
}

// Keep the native lock order: owner actor, exact session, then intent and
// immutable downstream state. All readers and writers serialize on intent.
func loadNativeInstallState(ctx context.Context, tx *sql.Tx, target nativeOwnerTarget, ref string, session [32]byte, peer nativeInstallPeer) (nativeInstallState, error) {
	var state nativeInstallState
	actor, sessionExpiry, err := lockedNativeOwnerSession(ctx, tx, session)
	if err != nil {
		return state, err
	}
	d, err := lockCurrentNativeOwnerDesignation(ctx, tx, target.Intent)
	if err != nil {
		return state, nativeOwnerUnavailable(err)
	}
	if d.Actor != actor || !sameNativeOwnerTarget(d.Target, target) {
		return state, nativeOwnerConsentUnavailable
	}
	receipt, original, display, err := loadNativeInstallConsent(ctx, tx, target, ref)
	if err != nil {
		return state, nativeOwnerUnavailable(err)
	}
	var branches []byte
	var historicalPeer nativeInstallPeer
	var historicalOwner int64
	err = tx.QueryRowContext(ctx, `SELECT d.legal_name,d.cuit,d.initiator_ref,d.claimant_ref,d.branch_plan,COALESCE(d.display_organization,''),
		COALESCE(i.install_ref,''),COALESCE(i.recorded_at,'epoch'::timestamptz),
		COALESCE(i.billing_issuer,''),COALESCE(i.gomar_recipient,''),COALESCE(i.planned_owner_id,0)
		FROM gomar.native_owner_install_drafts d LEFT JOIN gomar.native_owner_pending_installs i ON i.intent=d.intent
		WHERE d.intent=$1 AND d.consent_ref=$2`, target.Intent, ref).
		Scan(&state.draft.LegalName, &state.draft.CUIT, &state.draft.Initiator, &state.draft.Claimant, &branches, &state.draft.DisplayOrganization,
			&state.history.Ref, &state.history.RecordedAt, &historicalPeer.Issuer, &historicalPeer.Recipient, &historicalOwner)
	if err != nil {
		return nativeInstallState{}, nativeOwnerUnavailable(err)
	}
	now := nativeOwnerNow()
	if now.Unix() >= sessionExpiry {
		return nativeInstallState{}, nativeOwnerConsentUnavailable
	}
	if state.history.Ref != "" {
		if historicalPeer != peer || historicalOwner != actor {
			return nativeInstallState{}, nativeOwnerConsentUnavailable
		}
		state.history.RecordedAt, state.history.Dormant = state.history.RecordedAt.UTC(), true
		return state, nil
	}
	if !d.approvedAt(now) || d.ApprovalRef.String != receipt.ApprovalRef || !now.Before(receipt.ExpiresAt) ||
		subtle.ConstantTimeCompare(original, session[:]) != 1 {
		return nativeInstallState{}, nativeOwnerConsentUnavailable
	}
	state.draft.Target, state.draft.ConsentRef = target, ref
	if err = json.Unmarshal(branches, &state.draft.Branches); err != nil {
		return nativeInstallState{}, err
	}
	if canonical, ok := canonicalNativeInstallDraft(state.draft); ok && matchesNativeOwnerDisplay(display, actor, canonical) {
		state.draft = canonical
	} else {
		return nativeInstallState{}, nativeOwnerConsentUnavailable
	}
	state.owner, state.consent = actor, receipt
	state.validUntil = nativeOwnerDeadline(receipt.ExpiresAt, d.Expires, d.ApprovalExpires.Time, time.Unix(sessionExpiry, 0))
	return state, nil
}

func makeNativeInstallRequest(state nativeInstallState, peer nativeInstallPeer, nonce string) nativeInstallRequest {
	target := state.consent.Target
	target.Outlets = slices.Clone(target.Outlets)
	return nativeInstallRequest{Peer: peer, Purpose: nativeInstallPurpose, Schema: 1, Target: target,
		Initiator: state.draft.Initiator, Claimant: state.draft.Claimant, ConsentRef: state.consent.Ref,
		ApprovalRef: state.consent.ApprovalRef, Nonce: nonce}
}

func sameNativeInstallRequest(a, b nativeInstallRequest) bool {
	return a.Peer == b.Peer && a.Purpose == b.Purpose && a.Schema == b.Schema &&
		sameNativeOwnerTarget(a.Target, b.Target) && a.Initiator == b.Initiator && a.Claimant == b.Claimant &&
		a.ConsentRef == b.ConsentRef && a.ApprovalRef == b.ApprovalRef && a.Nonce == b.Nonce
}

// Unregistered. First read only local state and release all locks. Obtain
// the short-lived authenticated observation out of transaction. Recheck
// everything in a new native transaction before recording a dormant plan.
func (a *app) installNativeOwnerDormant(r *http.Request, expected nativeOwnerTarget, ref string, peer nativeInstallPeer, verify nativeInstallVerifier) (nativeInstallReceipt, error) {
	target, valid := canonicalNativeOwnerTarget(expected)
	if !valid || !nativeOwnerRef(ref, "accept-") || !nativeOwnerOpaqueID(peer.Issuer) || !nativeOwnerOpaqueID(peer.Recipient) {
		return nativeInstallReceipt{}, badInput
	}
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return nativeInstallReceipt{}, err
	}
	ctx := r.Context()
	initial, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nativeInstallReceipt{}, err
	}
	defer initial.Rollback()
	state, err := loadNativeInstallState(ctx, initial, target, ref, session, peer)
	if err != nil {
		return nativeInstallReceipt{}, err
	}
	if err = initial.Commit(); err != nil {
		return nativeInstallReceipt{}, err
	}
	if state.history.Ref != "" {
		return state.history, nil
	}
	if verify == nil {
		return nativeInstallReceipt{}, nativeOwnerConsentUnavailable
	}
	nonce := newProviderRandom()
	request := makeNativeInstallRequest(state, peer, nonce)
	started := nativeOwnerNow()
	limit := nativeOwnerDeadline(started.Add(nativeInstallWindow), state.validUntil)
	verifyContext, cancel := context.WithDeadline(ctx, limit)
	observation, err := verify(verifyContext, request)
	cancel()
	// Check the immutable original request, not a verifier-mutated slice.
	request = makeNativeInstallRequest(state, peer, nonce)
	now := nativeOwnerNow()
	if err != nil || !sameNativeInstallRequest(observation.Binding, request) ||
		observation.CheckedAt.Before(started) || observation.CheckedAt.After(now) ||
		!now.Before(observation.ExpiresAt) || !now.Before(limit) ||
		observation.ExpiresAt.After(observation.CheckedAt.Add(nativeInstallWindow)) {
		return nativeInstallReceipt{}, nativeOwnerConsentUnavailable
	}
	final := nativeOwnerDeadline(observation.ExpiresAt, limit)
	commitContext, stop := context.WithDeadline(ctx, final)
	defer stop()
	tx, err := a.db.BeginTx(commitContext, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nativeInstallReceipt{}, err
	}
	defer tx.Rollback()
	current, err := loadNativeInstallState(commitContext, tx, target, ref, session, peer)
	if err != nil {
		return nativeInstallReceipt{}, err
	}
	if current.history.Ref != "" {
		return current.history, tx.Commit()
	}
	if !sameNativeInstallRequest(observation.Binding, makeNativeInstallRequest(current, peer, nonce)) {
		return nativeInstallReceipt{}, nativeOwnerConsentUnavailable
	}
	// Do not claim an existing operational CUIT. A short NOWAIT check also
	// prevents a concurrent operational write between this check and commit.
	// It does not reserve the CUIT in operational tables after this commit.
	if _, err = tx.ExecContext(commitContext, `LOCK TABLE gomar.entities IN SHARE MODE NOWAIT`); err != nil {
		return nativeInstallReceipt{}, nativeOwnerConsentUnavailable
	}
	now = nativeOwnerNow()
	effective := nativeOwnerDeadline(final, current.validUntil)
	if !now.Before(effective) {
		return nativeInstallReceipt{}, nativeOwnerConsentUnavailable
	}
	record := nativeInstallReceipt{Ref: "pending-" + newProviderRandom(), RecordedAt: now, Dormant: true}
	nonceHash := sha256.Sum256([]byte(nonce))
	var inserted int
	err = tx.QueryRowContext(commitContext, `INSERT INTO gomar.native_owner_pending_installs
		(intent,consent_ref,install_ref,cuit,planned_owner_id,planned_role,billing_issuer,gomar_recipient,
			nonce_hash,decision_checked_at,decision_expires_at,recorded_at)
		SELECT $1,$2,$3,$4,$5,'administration',$6,$7,$8,$9,$10,$11
		WHERE NOT EXISTS (SELECT 1 FROM gomar.entities WHERE cuit=$4)
		ON CONFLICT DO NOTHING RETURNING 1`, target.Intent, ref, record.Ref, current.draft.CUIT, current.owner,
		peer.Issuer, peer.Recipient, nonceHash[:], observation.CheckedAt.UTC().Truncate(time.Microsecond),
		effective.UTC().Truncate(time.Microsecond), now).Scan(&inserted)
	if err != nil {
		return nativeInstallReceipt{}, nativeOwnerUnavailable(err)
	}
	for _, branch := range current.draft.Branches {
		if _, err = tx.ExecContext(commitContext, `INSERT INTO gomar.native_owner_pending_branches
			(install_ref,branch_ref,branch_name) VALUES($1,$2,$3)`, record.Ref, branch.Ref, branch.Name); err != nil {
			return nativeInstallReceipt{}, err
		}
	}
	if !nativeOwnerNow().Before(effective) {
		return nativeInstallReceipt{}, nativeOwnerConsentUnavailable
	}
	if err = tx.Commit(); errors.Is(err, context.DeadlineExceeded) {
		return nativeInstallReceipt{}, nativeOwnerConsentUnavailable
	}
	if err != nil {
		return nativeInstallReceipt{}, err
	}
	return record, nil
}
