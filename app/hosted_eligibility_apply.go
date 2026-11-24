package main

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"
)

const (
	hostedEligibilityPurpose = "hosted-eligibility-observation"
	hostedEligibilityWindow  = 5 * time.Second
)

var hostedEligibilityUnavailable = errors.New("hosted eligibility observation unavailable")

type hostedEligibilityBinding struct {
	issuer, audience, purpose, business string
	branches                            []string
}

type hostedEligibilityObservation struct {
	binding               hostedEligibilityBinding
	epoch                 int64
	state                 string
	checkedAt, validUntil time.Time
}

// A trusted verifier must already authenticate the configured source and
// correlate its entire response to this request before returning. These are
// internal values, not a wire format; a matching body, payment/provider
// revision, consent, or first-install observation cannot construct authority.
// No source is registered outside the synthetic fixture.
type hostedEligibilitySource struct {
	issuer, audience string
	verify           func(context.Context, hostedEligibilityBinding) (hostedEligibilityObservation, error)
}

type hostedEligibilityApplyResult uint8

const (
	hostedEligibilityNotRecorded hostedEligibilityApplyResult = iota
	hostedEligibilityRecorded
	hostedEligibilityReplay
	hostedEligibilityDisputed
	hostedEligibilityOlderPositive
)

func validHostedEligibilityScope(business string, branches []string) bool {
	if !nativeOwnerOpaqueID(business) || len(branches) == 0 || len(branches) > 64 {
		return false
	}
	for i, branch := range branches {
		if !nativeOwnerOpaqueID(branch) || i > 0 && branches[i-1] >= branch {
			return false
		}
	}
	return true
}

func validHostedEligibilityObservation(o hostedEligibilityObservation, expected hostedEligibilityBinding) bool {
	if o.binding.issuer != expected.issuer || o.binding.audience != expected.audience ||
		o.binding.purpose != expected.purpose || o.binding.business != expected.business ||
		!slices.Equal(o.binding.branches, expected.branches) || o.epoch <= 0 ||
		(o.state != "eligible" && o.state != "suspended" && o.state != "unknown") {
		return false
	}
	// Match the database's exact precision instead of rounding a source's
	// timestamps (which could conflate a conflict with a replay).
	for _, stamp := range []time.Time{o.checkedAt, o.validUntil} {
		if stamp.UTC().Year() < 1 || stamp.UTC().Year() > 9999 || stamp.Nanosecond()%1000 != 0 {
			return false
		}
	}
	return o.checkedAt.Before(o.validUntil) && !o.validUntil.After(o.checkedAt.Add(hostedEligibilityWindow))
}

func (f *hostedDiscoveryReadFixture) eligibilityWriteTime(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	if f.now != nil {
		return f.clock(), nil
	}
	var now time.Time
	err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

// Unregistered. The result describes historical application only; it does
// not authorize a read, renew a lease, or promise instantaneous revocation.
// Polling/transport is not installed; verification must finish before the
// local Read Committed transaction and exact scope lock are acquired.
func (a *app) applyHostedEligibilityObservation(ctx context.Context, business string, branches []string) (hostedEligibilityApplyResult, error) {
	f := a.hostedDiscoveryRead
	if f == nil || f.observationSource == nil || f.observationSource.verify == nil ||
		!validHostedEligibilityScope(business, branches) || !nativeOwnerOpaqueID(f.observationSource.issuer) ||
		!nativeOwnerOpaqueID(f.observationSource.audience) {
		return hostedEligibilityNotRecorded, hostedEligibilityUnavailable
	}
	source := f.observationSource
	expected := hostedEligibilityBinding{source.issuer, source.audience, hostedEligibilityPurpose, business, slices.Clone(branches)}
	request := expected
	request.branches = slices.Clone(expected.branches)
	o, err := source.verify(ctx, request)
	if err != nil || !validHostedEligibilityObservation(o, expected) {
		return hostedEligibilityNotRecorded, hostedEligibilityUnavailable
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return hostedEligibilityNotRecorded, err
	}
	defer tx.Rollback()
	var exact bool
	if err := tx.QueryRowContext(ctx, `SELECT branches=$2::text[] FROM gomar.discovery_paid_scope WHERE business_ref=$1 FOR UPDATE`, business, expected.branches).Scan(&exact); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = hostedEligibilityUnavailable
		}
		return hostedEligibilityNotRecorded, err
	}
	if !exact {
		return hostedEligibilityNotRecorded, hostedEligibilityUnavailable
	}
	// The immutable scope lock conflicts with dependent mapping inserts;
	// a separate statement sees all mappings committed before it was held.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(array_agg(branch_ref ORDER BY branch_ref COLLATE "C")=$2::text[],FALSE)
		FROM gomar.discovery_paid_outlet WHERE business_ref=$1`, business, expected.branches).Scan(&exact); err != nil {
		return hostedEligibilityNotRecorded, err
	}
	if !exact {
		return hostedEligibilityNotRecorded, hostedEligibilityUnavailable
	}
	var latest int64
	var sameEpoch, replay bool
	var latestCheck sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(epoch),0),COALESCE(BOOL_OR(epoch=$2),FALSE),
		COALESCE(BOOL_OR(epoch=$2 AND state=$3 AND verified_at=$4 AND valid_until=$5),FALSE),MAX(verified_at)
		FROM gomar.discovery_scope_epoch WHERE business_ref=$1`, business, o.epoch, o.state, o.checkedAt, o.validUntil).
		Scan(&latest, &sameEpoch, &replay, &latestCheck); err != nil {
		return hostedEligibilityNotRecorded, err
	}
	if replay {
		return hostedEligibilityReplay, nil
	}
	now, err := f.eligibilityWriteTime(ctx, tx)
	if err != nil {
		return hostedEligibilityNotRecorded, err
	}
	if o.checkedAt.After(now) {
		return hostedEligibilityNotRecorded, hostedEligibilityUnavailable
	}
	result := hostedEligibilityRecorded
	positive := o.state == "eligible" && !sameEpoch
	switch {
	case sameEpoch:
		// Retain a separately verified contradiction, even after the
		// original window. Readers cannot treat a disputed epoch as allow.
		result = hostedEligibilityDisputed
	case positive && o.epoch < latest:
		return hostedEligibilityOlderPositive, nil
	case positive && (!now.Before(o.validUntil) || latestCheck.Valid && !o.checkedAt.After(latestCheck.Time)):
		return hostedEligibilityNotRecorded, hostedEligibilityUnavailable
	}
	// Negative history is not a new allowance. Delayed evidence still
	// constrains any positive checked no later than that negative/dispute.
	if _, err = tx.ExecContext(ctx, `INSERT INTO gomar.discovery_scope_epoch
		(business_ref,branches,epoch,state,verified_at,valid_until) VALUES ($1,$2,$3,$4,$5,$6)`,
		business, expected.branches, o.epoch, o.state, o.checkedAt, o.validUntil); err != nil {
		return hostedEligibilityNotRecorded, err
	}
	if positive {
		now, err = f.eligibilityWriteTime(ctx, tx)
		if err != nil {
			return hostedEligibilityNotRecorded, err
		}
		if now.Before(o.checkedAt) || !now.Before(o.validUntil) {
			return hostedEligibilityNotRecorded, hostedEligibilityUnavailable
		}
	}
	if err = tx.Commit(); err != nil {
		return hostedEligibilityNotRecorded, err
	}
	return result, nil
}
