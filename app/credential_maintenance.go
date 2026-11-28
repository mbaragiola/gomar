package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"
)

const (
	browserSessionLimit  = 24
	recoverySessionLimit = 8
	mcpMemberKeyLimit    = 32
	mcpEntityKeyLimit    = 128
	credentialPruneBatch = 100
	credentialAuditDays  = 90
)

var tooManySessions = problem{429, "Alcanzaste el límite de sesiones. Cerrá una sesión anterior o esperá a que venza."}
var tooManyMCPReadKeys = problem{429, "Se alcanzó el límite de claves de lectura. Revocá una clave anterior o esperá a que venza."}

// The caller holds the account write lock and has removed the cookie being replaced.
func requireSessionCapacity(ctx context.Context, tx *sql.Tx, userID, now int64, recovery bool) error {
	table, limit := "sessions", browserSessionLimit
	if recovery {
		table, limit = "admin_recovery_sessions", recoverySessionLimit
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE user_id=$1 AND expires>$2", userID, now).Scan(&count); err != nil {
		return err
	}
	if count >= limit {
		return tooManySessions
	}
	return nil
}

// The caller holds the entity write lock until the new key is committed.
func requireMCPReadKeyCapacity(ctx context.Context, tx *sql.Tx, entityID, userID, now int64) error {
	var member, entity int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE user_id=$2),COUNT(*)
		FROM mcp_read_tokens WHERE entity_id=$1 AND expires_at>$3`, entityID, userID, now).Scan(&member, &entity); err != nil {
		return err
	}
	if member >= mcpMemberKeyLimit || entity >= mcpEntityKeyLimit {
		return tooManyMCPReadKeys
	}
	return nil
}

func (a *app) credentialPruneLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := a.pruneCredentials(pass, time.Now())
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Print("could not finish expired credential pruning; will retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *app) pruneCredentials(ctx context.Context, now time.Time) error {
	var pruneErr error
	for _, target := range []struct{ table, id, expiry string }{
		{"sessions", "token_hash", "expires"},
		{"admin_recovery_sessions", "token_hash", "expires"},
		{"mcp_read_tokens", "id", "expires_at"},
	} {
		var pending bool
		err := a.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+target.table+" WHERE "+target.expiry+"<=$1)", now.Unix()).Scan(&pending)
		if err == nil && pending {
			// Delete and count the same rows atomically. SKIP LOCKED lets
			// sign-out, password change and other workers proceed.
			query := `WITH batch AS (
				SELECT ` + target.id + ` FROM ` + target.table + ` WHERE ` + target.expiry + `<=$1
				ORDER BY ` + target.expiry + `,` + target.id + ` LIMIT $2 FOR UPDATE SKIP LOCKED
			), removed AS (
				DELETE FROM ` + target.table + ` c USING batch WHERE c.` + target.id + `=batch.` + target.id + ` RETURNING 1
			)
			INSERT INTO credential_prune_audit(utc_day,kind,removed)
			SELECT $3,$4,COUNT(*) FROM removed HAVING COUNT(*)>0
			ON CONFLICT(utc_day,kind) DO UPDATE SET removed=credential_prune_audit.removed+EXCLUDED.removed`
			_, err = a.db.ExecContext(ctx, query, now.Unix(), credentialPruneBatch, now.UTC().Format(time.DateOnly), target.table)
		}
		pruneErr = errors.Join(pruneErr, err)
	}
	cutoff := now.UTC().AddDate(0, 0, -credentialAuditDays).Format(time.DateOnly)
	var auditExpired bool
	err := a.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM credential_prune_audit WHERE utc_day<$1)", cutoff).Scan(&auditExpired)
	if err == nil && auditExpired {
		_, err = a.db.ExecContext(ctx, `WITH old AS (
			SELECT utc_day,kind FROM credential_prune_audit WHERE utc_day<$1
			ORDER BY utc_day,kind LIMIT $2 FOR UPDATE SKIP LOCKED)
			DELETE FROM credential_prune_audit a USING old WHERE a.utc_day=old.utc_day AND a.kind=old.kind`, cutoff, credentialPruneBatch)
	}
	return errors.Join(pruneErr, err)
}
