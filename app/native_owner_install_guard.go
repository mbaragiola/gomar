package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"
)

func loadDormantNativePlan(ctx context.Context, tx *sql.Tx, target nativeOwnerTarget, consentRef, installRef string, session [32]byte, peer nativeInstallPeer) (nativeInstallState, error) {
	state, err := loadNativeInstallState(ctx, tx, target, consentRef, session, peer, installRef)
	if err != nil {
		return nativeInstallState{}, err
	}
	var owner int64
	var cuit, role string
	// A concurrent branch addition would need the pending parent's foreign
	// key lock. Read the complete branch set after this lock.
	err = tx.QueryRowContext(ctx, `SELECT planned_owner_id,cuit,planned_role
		FROM gomar.native_owner_pending_installs
		WHERE install_ref=$1 AND intent=$2 AND consent_ref=$3 FOR UPDATE`, installRef, target.Intent, consentRef).
		Scan(&owner, &cuit, &role)
	if err != nil {
		return nativeInstallState{}, nativeOwnerUnavailable(err)
	}
	if owner != state.owner || cuit != state.draft.CUIT || role != "administration" {
		return nativeInstallState{}, nativeOwnerConsentUnavailable
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT json_agg(json_build_object('ref',branch_ref,'name',branch_name) ORDER BY branch_ref)
		FROM gomar.native_owner_pending_branches WHERE install_ref=$1`, installRef).Scan(&raw)
	if err != nil {
		return nativeInstallState{}, err
	}
	var branches []nativeInstallBranch
	if json.Unmarshal(raw, &branches) != nil || !slices.Equal(state.draft.Branches, branches) || time.Until(state.validUntil) <= 0 {
		return nativeInstallState{}, nativeOwnerConsentUnavailable
	}
	return state, nil
}

// Unregistered and read-only. Success is evidence that the dormant plan
// passed this check. It confers no operational access or future approval.
func (a *app) checkDormantNativeInstall(r *http.Request, expected nativeOwnerTarget, consentRef, installRef string, peer nativeInstallPeer, verify nativeInstallVerifier) (resultErr error) {
	target, valid := canonicalNativeOwnerTarget(expected)
	if !valid || !nativeOwnerRef(consentRef, "accept-") || !nativeOwnerRef(installRef, "pending-") ||
		!nativeOwnerOpaqueID(peer.Issuer) || !nativeOwnerOpaqueID(peer.Recipient) {
		return badInput
	}
	session, err := a.nativeOwnerBrowser(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	options := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	initial, err := a.db.BeginTx(ctx, options)
	if err != nil {
		return err
	}
	defer initial.Rollback()
	state, err := loadDormantNativePlan(ctx, initial, target, consentRef, installRef, session, peer)
	if err != nil {
		return err
	}
	if err = initial.Commit(); err != nil {
		return err
	}
	observation, nonce, final, err := observeNativeInstall(ctx, state, peer, verify)
	if err != nil {
		return err
	}
	finalCtx, cancel := context.WithDeadline(ctx, final)
	defer func() {
		if errors.Is(finalCtx.Err(), context.DeadlineExceeded) {
			resultErr = nativeOwnerConsentUnavailable
		}
		cancel()
	}()
	tx, err := a.db.BeginTx(finalCtx, options)
	if err != nil {
		return nativeOwnerUnavailable(err)
	}
	defer tx.Rollback()
	current, err := loadDormantNativePlan(finalCtx, tx, target, consentRef, installRef, session, peer)
	if err != nil {
		return err
	}
	if !sameNativeInstallRequest(observation.Binding, makeNativeInstallRequest(current, peer, nonce)) {
		return nativeOwnerConsentUnavailable
	}
	if _, err = tx.ExecContext(finalCtx, `LOCK TABLE gomar.entities IN SHARE MODE NOWAIT`); err != nil {
		return nativeOwnerConsentUnavailable
	}
	var occupied bool
	err = tx.QueryRowContext(finalCtx, `SELECT EXISTS(SELECT 1 FROM gomar.entities WHERE cuit=$1)`, current.draft.CUIT).Scan(&occupied)
	if err != nil {
		return nativeOwnerUnavailable(err)
	}
	if occupied || !nativeOwnerNow().Before(nativeOwnerDeadline(current.validUntil, final)) {
		return nativeOwnerConsentUnavailable
	}
	return nativeOwnerUnavailable(tx.Commit())
}
