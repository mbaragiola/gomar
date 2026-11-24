package main

// This file is compiled only by tests. There is deliberately no production
// schema, runtime caller, hosted middleware, or enabling action.
import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"
)

const fixtureStagePurpose = "stage-inactive-owner-qa"
const fixtureStageLock = 410420234

const fixtureStageDDL = `
CREATE TABLE gomar.fixture_staged_activations (
    install_ref TEXT PRIMARY KEY REFERENCES gomar.native_owner_pending_installs(install_ref),
    receipt_ref TEXT NOT NULL UNIQUE,
    consent_ref TEXT NOT NULL REFERENCES gomar.native_owner_receipts(receipt_ref),
    entity_id BIGINT NOT NULL UNIQUE REFERENCES gomar.entities(id),
    member_id BIGINT NOT NULL UNIQUE REFERENCES gomar.memberships(id),
    owner_id BIGINT NOT NULL REFERENCES gomar.users(id),
    recorded_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE gomar.fixture_staged_branches (
    receipt_ref TEXT NOT NULL REFERENCES gomar.fixture_staged_activations(receipt_ref),
    branch_ref TEXT NOT NULL,
    outlet_id BIGINT NOT NULL UNIQUE REFERENCES gomar.outlets(id),
    PRIMARY KEY(receipt_ref,branch_ref)
);
CREATE TRIGGER fixture_stage_receipts_immutable BEFORE UPDATE OR DELETE ON gomar.fixture_staged_activations
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();
CREATE TRIGGER fixture_stage_branches_immutable BEFORE UPDATE OR DELETE ON gomar.fixture_staged_branches
    FOR EACH ROW EXECUTE FUNCTION gomar.gomar_reject_history_mutation();

-- Quarantine only the synthetic installed scope. A fixture-wide lock makes
-- entry into this trigger serialize with staging; the guarded lookup uses
-- a fresh Read Committed statement snapshot after that lock.
CREATE FUNCTION gomar.fixture_stage_quarantine() RETURNS TRIGGER LANGUAGE plpgsql AS $guard$
DECLARE
    previous JSONB:=CASE WHEN TG_OP='INSERT' THEN '{}'::jsonb ELSE to_jsonb(OLD) END;
    proposed JSONB:=CASE WHEN TG_OP='DELETE' THEN '{}'::jsonb ELSE to_jsonb(NEW) END;
    blocked BOOLEAN;
BEGIN
    IF current_setting('transaction_isolation')<>'read committed' THEN
        RAISE EXCEPTION 'fixture quarantine requires Read Committed' USING ERRCODE='23514';
    END IF;
    IF NOT pg_try_advisory_xact_lock(410420234) THEN
        RAISE EXCEPTION 'synthetic staging quarantine busy' USING ERRCODE='23514';
    END IF;
    IF TG_TABLE_NAME='entities' THEN
        SELECT EXISTS(SELECT 1 FROM gomar.fixture_staged_activations s
            WHERE s.entity_id IN ((previous->>'id')::bigint,(proposed->>'id')::bigint)) INTO blocked;
    ELSIF TG_TABLE_NAME='outlets' OR TG_TABLE_NAME='memberships' THEN
        SELECT EXISTS(SELECT 1 FROM gomar.fixture_staged_activations s
            WHERE s.entity_id IN ((previous->>'entity_id')::bigint,(proposed->>'entity_id')::bigint)) INTO blocked;
    ELSIF TG_TABLE_NAME='outlet_grants' THEN
        SELECT EXISTS(SELECT 1 FROM gomar.fixture_staged_activations s
            WHERE EXISTS (SELECT 1 FROM gomar.memberships m WHERE m.entity_id=s.entity_id
                  AND m.id IN ((previous->>'membership_id')::bigint,(proposed->>'membership_id')::bigint))
               OR EXISTS (SELECT 1 FROM gomar.outlets o WHERE o.entity_id=s.entity_id
                  AND o.id IN ((previous->>'outlet_id')::bigint,(proposed->>'outlet_id')::bigint))) INTO blocked;
    END IF;
    IF blocked THEN
        RAISE EXCEPTION 'synthetic staged scope cannot become operational' USING ERRCODE='23514';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$guard$;
CREATE TRIGGER fixture_stage_entity_gate BEFORE INSERT OR UPDATE OR DELETE ON gomar.entities
    FOR EACH ROW EXECUTE FUNCTION gomar.fixture_stage_quarantine();
CREATE TRIGGER fixture_stage_outlet_gate BEFORE INSERT OR UPDATE OR DELETE ON gomar.outlets
    FOR EACH ROW EXECUTE FUNCTION gomar.fixture_stage_quarantine();
CREATE TRIGGER fixture_stage_member_gate BEFORE INSERT OR UPDATE OR DELETE ON gomar.memberships
    FOR EACH ROW EXECUTE FUNCTION gomar.fixture_stage_quarantine();
CREATE TRIGGER fixture_stage_grant_gate BEFORE INSERT OR UPDATE OR DELETE ON gomar.outlet_grants
    FOR EACH ROW EXECUTE FUNCTION gomar.fixture_stage_quarantine();
`

type fixtureStageInput struct {
	Target                 nativeOwnerTarget
	ConsentRef, InstallRef string
}

type fixtureStageBinding struct {
	Input                    fixtureStageInput
	Purpose, Approval, Nonce string
}

type fixtureStageAuthority struct {
	Binding    fixtureStageBinding
	At, Before time.Time
}

type fixtureStageVerifier func(context.Context, fixtureStageBinding) (fixtureStageAuthority, error)

type fixtureStageBranch struct {
	BranchRef string `json:"branch_ref"`
	OutletID  int64  `json:"outlet_id"`
}

type fixtureStageReceipt struct {
	Ref                string
	EntityID, MemberID int64
	Branches           []fixtureStageBranch
	RecordedAt         time.Time
	Inactive           bool
}

type fixtureStageState struct {
	Owner    int64
	Law      nativeInstallDraft
	Approval string
	Deadline time.Time
	History  fixtureStageReceipt
}

func fixtureStageSame(a, b fixtureStageBinding) bool {
	return a.Purpose == b.Purpose && a.Approval == b.Approval && a.Nonce == b.Nonce &&
		a.Input.ConsentRef == b.Input.ConsentRef && a.Input.InstallRef == b.Input.InstallRef &&
		sameNativeOwnerTarget(a.Input.Target, b.Input.Target)
}

func loadFixtureStage(ctx context.Context, tx *sql.Tx, input fixtureStageInput, session [32]byte) (fixtureStageState, error) {
	var state fixtureStageState
	owner, sessionExpiry, err := lockedNativeOwnerSession(ctx, tx, session)
	if err != nil {
		return state, err
	}
	d, err := lockCurrentNativeOwnerDesignation(ctx, tx, input.Target.Intent)
	if err != nil {
		return state, nativeOwnerUnavailable(err)
	}
	if d.Actor != owner || !sameNativeOwnerTarget(d.Target, input.Target) {
		return state, nativeOwnerConsentUnavailable
	}
	native, original, err := loadNativeInstallConsent(ctx, tx, input.Target, input.ConsentRef)
	if err != nil {
		return state, nativeOwnerUnavailable(err)
	}
	var cuit, role string
	var planOwner int64
	// Lock the parent row. An extra-branch insert also needs its foreign
	// key lock, which conflicts with this FOR UPDATE until commit.
	err = tx.QueryRowContext(ctx, `SELECT cuit,planned_role,planned_owner_id FROM gomar.native_owner_pending_installs
		WHERE install_ref=$1 AND intent=$2 AND consent_ref=$3 FOR UPDATE`, input.InstallRef, input.Target.Intent, input.ConsentRef).
		Scan(&cuit, &role, &planOwner)
	if err != nil || role != "administration" || planOwner != owner {
		if err != nil {
			return state, nativeOwnerUnavailable(err)
		}
		return state, nativeOwnerConsentUnavailable
	}
	var approved, pending []byte
	state.Law.Target, state.Law.ConsentRef = input.Target, input.ConsentRef
	err = tx.QueryRowContext(ctx, `SELECT legal_name,cuit,initiator_ref,claimant_ref,branch_plan,
		(SELECT json_agg(json_build_object('ref',b.branch_ref,'name',b.branch_name) ORDER BY b.branch_ref)
		 FROM gomar.native_owner_pending_branches b WHERE b.install_ref=$3)
		FROM gomar.native_owner_install_drafts WHERE intent=$1 AND consent_ref=$2`, input.Target.Intent, input.ConsentRef, input.InstallRef).
		Scan(&state.Law.LegalName, &state.Law.CUIT, &state.Law.Initiator, &state.Law.Claimant, &approved, &pending)
	if err != nil {
		return state, nativeOwnerUnavailable(err)
	}
	var actual []nativeInstallBranch
	if json.Unmarshal(approved, &state.Law.Branches) != nil || json.Unmarshal(pending, &actual) != nil {
		return state, nativeOwnerConsentUnavailable
	}
	law, valid := canonicalNativeInstallDraft(state.Law)
	if !valid || law.CUIT != cuit || !slices.Equal(law.Branches, actual) {
		return state, nativeOwnerConsentUnavailable
	}
	state.Law, state.Owner, state.Approval = law, owner, native.ApprovalRef
	now := nativeOwnerNow()
	if now.Unix() >= sessionExpiry {
		return state, nativeOwnerConsentUnavailable
	}
	var installed []byte
	err = tx.QueryRowContext(ctx, `SELECT s.receipt_ref,s.entity_id,s.member_id,s.recorded_at,
		(SELECT json_agg(json_build_object('branch_ref',b.branch_ref,'outlet_id',b.outlet_id) ORDER BY b.branch_ref)
		 FROM gomar.fixture_staged_branches b WHERE s.receipt_ref=b.receipt_ref)
		FROM gomar.fixture_staged_activations s WHERE s.install_ref=$1 AND s.consent_ref=$2 AND s.owner_id=$3`,
		input.InstallRef, input.ConsentRef, owner).
		Scan(&state.History.Ref, &state.History.EntityID, &state.History.MemberID, &state.History.RecordedAt, &installed)
	if err == nil {
		if json.Unmarshal(installed, &state.History.Branches) != nil {
			return state, nativeOwnerConsentUnavailable
		}
		state.History.RecordedAt, state.History.Inactive = state.History.RecordedAt.UTC(), true
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	if !d.approvedAt(now) || d.ApprovalRef.String != native.ApprovalRef || !now.Before(native.ExpiresAt) ||
		subtle.ConstantTimeCompare(original, session[:]) != 1 {
		return state, nativeOwnerConsentUnavailable
	}
	state.Deadline = nativeOwnerDeadline(native.ExpiresAt, d.Expires, d.ApprovalExpires.Time, time.Unix(sessionExpiry, 0))
	return state, nil
}

func fixtureStageNativeOwner(a *app, r *http.Request, input fixtureStageInput, verify fixtureStageVerifier) (fixtureStageReceipt, error) {
	var denied fixtureStageReceipt
	target, ok := canonicalNativeOwnerTarget(input.Target)
	if !ok || !nativeOwnerRef(input.ConsentRef, "accept-") || !nativeOwnerRef(input.InstallRef, "pending-") {
		return denied, badInput
	}
	input.Target = target
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return denied, err
	}
	ctx := r.Context()
	options := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	preflight, err := a.db.BeginTx(ctx, options)
	if err != nil {
		return denied, err
	}
	defer preflight.Rollback()
	initial, err := loadFixtureStage(ctx, preflight, input, session)
	if err != nil {
		return denied, err
	}
	if err = preflight.Commit(); err != nil {
		return denied, err
	}
	if initial.History.Ref != "" {
		return initial.History, nil
	}
	if verify == nil {
		return denied, nativeOwnerConsentUnavailable
	}
	nonce := newProviderRandom()
	binding := fixtureStageBinding{Input: input, Purpose: fixtureStagePurpose, Approval: initial.Approval, Nonce: nonce}
	binding.Input.Target.Outlets = slices.Clone(target.Outlets)
	start := nativeOwnerNow()
	limit := nativeOwnerDeadline(start.Add(4*time.Second), initial.Deadline)
	outside, cancel := context.WithDeadline(ctx, limit)
	authority, err := verify(outside, binding)
	cancel()
	binding.Input.Target.Outlets = slices.Clone(target.Outlets)
	now := nativeOwnerNow()
	if err != nil || !fixtureStageSame(binding, authority.Binding) || authority.At.Before(start) || authority.At.After(now) ||
		!now.Before(authority.Before) || authority.Before.After(authority.At.Add(4*time.Second)) {
		return denied, nativeOwnerConsentUnavailable
	}
	limit = nativeOwnerDeadline(limit, authority.Before)
	finalCtx, stop := context.WithDeadline(ctx, limit)
	defer stop()
	tx, err := a.db.BeginTx(finalCtx, options)
	if err != nil {
		return denied, err
	}
	defer tx.Rollback()
	state, err := loadFixtureStage(finalCtx, tx, input, session)
	if err != nil {
		return denied, err
	}
	if state.History.Ref != "" {
		return state.History, tx.Commit()
	}
	if state.Approval != binding.Approval {
		return denied, nativeOwnerConsentUnavailable
	}
	// All synthetic operational triggers use this lock. No operational
	// write can race publication of the quarantine record.
	if _, err = tx.ExecContext(finalCtx, `SELECT pg_advisory_xact_lock($1)`, fixtureStageLock); err != nil {
		return denied, err
	}
	limit = nativeOwnerDeadline(limit, state.Deadline)
	now = nativeOwnerNow()
	if !now.Before(limit) {
		return denied, nativeOwnerConsentUnavailable
	}
	result := fixtureStageReceipt{Ref: "stage-" + newProviderRandom(), RecordedAt: now, Inactive: true}
	err = tx.QueryRowContext(finalCtx, `INSERT INTO gomar.entities(name,cuit,active) VALUES($1,$2,false)
		ON CONFLICT (cuit) DO NOTHING RETURNING id`, state.Law.LegalName, state.Law.CUIT).Scan(&result.EntityID)
	if err != nil {
		return denied, nativeOwnerUnavailable(err)
	}
	err = tx.QueryRowContext(finalCtx, `INSERT INTO gomar.memberships(user_id,entity_id,role,active)
		VALUES($1,$2,'administration',false) RETURNING id`, state.Owner, result.EntityID).Scan(&result.MemberID)
	if err != nil {
		return denied, err
	}
	for _, branch := range state.Law.Branches {
		row := fixtureStageBranch{BranchRef: branch.Ref}
		err = tx.QueryRowContext(finalCtx, `INSERT INTO gomar.outlets(entity_id,name,active) VALUES($1,$2,false) RETURNING id`,
			result.EntityID, branch.Name).Scan(&row.OutletID)
		if err != nil {
			return denied, err
		}
		if _, err = tx.ExecContext(finalCtx, `INSERT INTO gomar.outlet_grants(membership_id,outlet_id) VALUES($1,$2)`, result.MemberID, row.OutletID); err != nil {
			return denied, err
		}
		result.Branches = append(result.Branches, row)
	}
	// Insert quarantine and history together. No committed operational
	// row ever exists outside the synthetic quarantine.
	_, err = tx.ExecContext(finalCtx, `INSERT INTO gomar.fixture_staged_activations
		(install_ref,receipt_ref,consent_ref,entity_id,member_id,owner_id,recorded_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, input.InstallRef, result.Ref, input.ConsentRef, result.EntityID, result.MemberID, state.Owner, now)
	if err != nil {
		return denied, err
	}
	for _, row := range result.Branches {
		_, err = tx.ExecContext(finalCtx, `INSERT INTO gomar.fixture_staged_branches(receipt_ref,branch_ref,outlet_id) VALUES($1,$2,$3)`,
			result.Ref, row.BranchRef, row.OutletID)
		if err != nil {
			return denied, err
		}
	}
	if !nativeOwnerNow().Before(limit) {
		return denied, nativeOwnerConsentUnavailable
	}
	if err = tx.Commit(); err != nil {
		return denied, err
	}
	return result, nil
}
