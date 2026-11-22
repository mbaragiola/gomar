package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

type reconciliationDraftScope struct{ Actor, Entity, Outlet int64 }

type reconciliationDraftInput struct {
	MatchReference bool `json:"match_reference"`
}

type reconciliationDraftPreview struct {
	Scope reconciliationDraftScope
	Input reconciliationDraftInput
}

func (p reconciliationDraftPreview) digest() [32]byte {
	data, _ := json.Marshal(struct {
		Purpose string
		Preview reconciliationDraftPreview
	}{"reconciliation_draft.v1", p})
	return sha256.Sum256(data)
}

// prepareReconciliationDraft performs no writes. Its caller must supply the
// current authenticated actor and the exact selected entity and outlet.
func prepareReconciliationDraft(tx *sql.Tx, scope reconciliationDraftScope, in reconciliationDraftInput) (reconciliationDraftPreview, error) {
	if scope.Actor <= 0 || scope.Entity <= 0 || scope.Outlet <= 0 {
		return reconciliationDraftPreview{}, badInput
	}
	m, err := scopedMembership(tx, scope.Actor, scope.Entity, "reconcile_financial_data")
	if err != nil {
		return reconciliationDraftPreview{}, err
	}
	if err = requireSelectedDocumentOutlet(tx, m, scope.Outlet); err != nil {
		return reconciliationDraftPreview{}, err
	}
	return reconciliationDraftPreview{Scope: scope, Input: in}, nil
}

// applyReconciliationDraftApproval is intentionally not a handler. A future
// caller must verify and lock the issuing browser session and pending approval
// record in this same transaction, then record the resulting ID atomically.
// The typed contract creates an empty draft and cannot attach financial data.
func (a *app) applyReconciliationDraftApproval(ctx context.Context, tx *sql.Tx, u user, scope reconciliationDraftScope, p reconciliationDraftPreview, approved [32]byte) (reconciliationReadRun, error) {
	if u.id == 0 || u.id != scope.Actor {
		return reconciliationReadRun{}, unauthenticated
	}
	fingerprint := p.digest()
	if p.Scope != scope || subtle.ConstantTimeCompare(fingerprint[:], approved[:]) != 1 {
		return reconciliationReadRun{}, problem{409, "La propuesta de conciliación cambió."}
	}
	actor, err := lockReconciliationDraftScope(ctx, tx, scope)
	if err != nil {
		return reconciliationReadRun{}, err
	}
	u.name = actor
	return a.insertReconciliationDraft(tx, u, scope.Entity, []int64{scope.Outlet}, p.Input.MatchReference)
}

func lockReconciliationDraftScope(ctx context.Context, tx *sql.Tx, scope reconciliationDraftScope) (string, error) {
	var actor string
	var member int64
	var role string
	err := tx.QueryRowContext(ctx, `SELECT m.id,m.role,u.username FROM memberships m
 JOIN entities e ON e.id=m.entity_id AND e.active
 JOIN users u ON u.id=m.user_id AND u.active
 WHERE m.entity_id=$1 AND m.user_id=$2 AND m.active FOR SHARE OF m,e,u`,
		scope.Entity, scope.Actor).Scan(&member, &role, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", missingEntity
	}
	if err != nil {
		return "", err
	}
	if !slices.Contains(capabilities[role], "reconcile_financial_data") {
		return "", forbiddenRole
	}
	var outlet int64
	err = tx.QueryRowContext(ctx, `SELECT o.id FROM outlets o
 JOIN outlet_grants g ON g.outlet_id=o.id AND g.membership_id=$1
 WHERE o.entity_id=$2 AND o.id=$3 AND o.active FOR SHARE OF o,g`,
		member, scope.Entity, scope.Outlet).Scan(&outlet)
	if errors.Is(err, sql.ErrNoRows) {
		return "", missingOutlet
	}
	if err != nil {
		return "", err
	}
	return actor, nil
}
