package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// Only the injected barcode lane uses this native pre-lock. The existing
// protected middleware still authenticates the exact browser and CSRF before
// it runs; it does not confer MCP authority or accept an alternate identity.
func lockHostedNativeBarcodeBrowser(r *http.Request, tx *sql.Tx, u user) error {
	if u.employee {
		return forbiddenRole
	}
	ctx := r.Context()
	var actor int64
	// Freeze account/password updates without blocking the actor FK
	// KEY SHARE taken by an existing entity-first membership audit.
	if err := tx.QueryRowContext(ctx, `SELECT id FROM gomar.users WHERE id=$1 AND active AND password=$2 FOR SHARE`, u.id, u.password).Scan(&actor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return unauthenticated
		}
		return err
	}
	// Actor first, then the same exact ordinary login, before entity, outlet
	// or paid scope. Provider rotation and password resets lock in this order.
	hash := sha256.Sum256([]byte(cookieValue(r, "sessionid")))
	digest := sha256.Sum256([]byte(u.password))
	var expiry int64
	err := tx.QueryRowContext(ctx, `SELECT expires FROM gomar.sessions
		WHERE user_id=$1 AND token_hash=$2 AND password_digest=$3 AND auth_method<>'employee'
		AND (NOT $4 OR auth_method='provider') FOR SHARE`, actor, hash[:], digest[:], requiresProviderSession(r)).Scan(&expiry)
	if errors.Is(err, sql.ErrNoRows) || err == nil && time.Now().Unix() >= expiry {
		return unauthenticated
	}
	return err
}

type hostedPOSBarcodeLease struct {
	fixture               *hostedDiscoveryReadFixture
	checkedAt, validUntil time.Time
}

// Use the existing whole-scope predicate, including the exact grant, unique
// winning epoch and adverse/dispute chronology. The paid key below is checked
// against the outlet in the same statement; nothing from the key is returned.
const hostedPOSBarcodeAdmissionSQL = `
WITH discovery_input(actor_id,entity_id,outlet_id,checked_at,native_entity_list) AS (
    VALUES ($1::bigint,$2::bigint,$3::bigint,$4::timestamptz,FALSE)
), ` + hostedScopeEligibilitySQL + `
SELECT e.verified_at,e.valid_until FROM discovery_admitted d
JOIN gomar.discovery_paid_outlet p ON (p.outlet_id,p.entity_id)=(d.outlet_id,d.entity_id)
JOIN eligible e USING (business_ref)
WHERE d.entity_id=$2 AND d.outlet_id=$3 AND e.business_ref=$5`

// Enter only after the existing local actor/session, entity, membership,
// outlet and grant checks. The exact paid-scope SHARE lock is held through
// commit/rollback and conflicts with #105's UPDATE lock. That writer does not
// take these local operational locks, so it cannot reverse the order.
func (a *app) lockHostedPOSBarcode(ctx context.Context, tx *sql.Tx, s posRenameScope) (hostedPOSBarcodeLease, error) {
	f := a.hostedDiscoveryRead
	if f == nil {
		return hostedPOSBarcodeLease{}, nil
	}
	var class string
	var mapped sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT c.class,p.business_ref FROM gomar.discovery_entity_class c
		LEFT JOIN gomar.discovery_paid_outlet p ON p.entity_id=c.entity_id AND p.outlet_id=$2
		WHERE c.entity_id=$1`, s.Entity, s.Outlet).Scan(&class, &mapped)
	if errors.Is(err, sql.ErrNoRows) {
		return hostedPOSBarcodeLease{}, missingOutlet
	}
	if err != nil {
		return hostedPOSBarcodeLease{}, err
	}
	if class == "self_host" {
		return hostedPOSBarcodeLease{}, nil
	}
	if class != "paid_hosted" || !mapped.Valid {
		return hostedPOSBarcodeLease{}, missingOutlet
	}
	var key string
	err = tx.QueryRowContext(ctx, `SELECT business_ref FROM gomar.discovery_paid_scope WHERE business_ref=$1 FOR SHARE`, mapped.String).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return hostedPOSBarcodeLease{}, missingOutlet
	}
	if err != nil {
		return hostedPOSBarcodeLease{}, err
	}
	// Only sample the clock and visibility after the exact scope lock. The
	// subsequent statement sees any observation that committed while waiting.
	now, err := f.eligibilityWriteTime(ctx, tx)
	if err != nil {
		return hostedPOSBarcodeLease{}, err
	}
	l := hostedPOSBarcodeLease{fixture: f}
	err = tx.QueryRowContext(ctx, hostedPOSBarcodeAdmissionSQL, s.Actor, s.Entity, s.Outlet, now, key).Scan(&l.checkedAt, &l.validUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return hostedPOSBarcodeLease{}, missingOutlet
	}
	if err != nil {
		return hostedPOSBarcodeLease{}, err
	}
	return l, nil
}

func (a *app) hostedPOSCatalogLease(ctx context.Context, tx *sql.Tx, s posRenameScope, op posCatalogOperation) (hostedPOSBarcodeLease, error) {
	if op != posCatalogBarcode {
		return hostedPOSBarcodeLease{}, nil
	}
	return a.lockHostedPOSBarcode(ctx, tx, s)
}

func (l hostedPOSBarcodeLease) recheck(ctx context.Context, tx *sql.Tx) error {
	if l.fixture == nil {
		return nil
	}
	now, err := l.fixture.eligibilityWriteTime(ctx, tx)
	if err != nil {
		return err
	}
	if now.Before(l.checkedAt) || !now.Before(l.validUntil) {
		return missingOutlet
	}
	return nil
}

func (l hostedPOSBarcodeLease) commit(ctx context.Context, tx *sql.Tx) error {
	if err := l.recheck(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Keep other catalog actions on their original transaction path.
func (a *app) hostedPOSCatalogTxOptions(op posCatalogOperation) *sql.TxOptions {
	if a.hostedDiscoveryRead != nil && op == posCatalogBarcode {
		return &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return nil
}
