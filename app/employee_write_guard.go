package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type employeeLoginContextKey struct{}
type providerEmployeeContextKey struct{}

func mcpAccountAllowed(ctx context.Context, password string) bool {
	hybrid := ctx.Value(providerEmployeeContextKey{}) == true
	return !isEmployeeAnchor(password) && (!hybrid || password == providerOnlyPassword)
}

func employeeLoginAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(employeeLoginContextKey{}).(bool)
	return allowed
}

// Never opt employees into a new mutation by accident. All listed domain
// writes enter beginEmployeeCheckedWrite. Inspect/preview only read.
func employeeBrowserScope(r *http.Request, u user) error {
	if raw := r.PathValue("entity"); raw != "" {
		id, err := pathID(r, "entity")
		if err != nil || id != u.employeeOrg {
			return missingEntity
		}
	}
	if safeMethod(r.Method) {
		return nil
	}
	const e = "/api/v1/entities/{entity}"
	switch r.Method + " " + r.Pattern {
	case "POST /api/v1/auth/logout", "POST " + tenantSalesExportPagePath,
		"POST " + e + "/suppliers", "PUT " + e + "/suppliers/{supplier}",
		"POST " + e + "/documents", "PUT " + e + "/documents/{document}", "DELETE " + e + "/documents/{document}",
		"POST " + e + "/documents/{document}/review", "POST " + e + "/documents/{document}/attachments",
		"POST " + e + "/outlets/{outlet}/removed-drafts/{document}/restore",
		"POST " + e + "/outlets/{outlet}/intake", "DELETE " + e + "/intake/{operation}",
		"POST " + e + "/documents/{document}/payable", "POST " + e + "/payments",
		"POST " + e + "/payments/{payment}/post", "POST " + e + "/payments/{payment}/reverse", "POST " + e + "/payments/{payment}/receipt",
		"POST " + posPath + "/sales", "POST " + stockPath + "/products/{product}/movements":
		return nil
	default:
		return forbiddenRole
	}
}

// Employee writes use the existing domain transaction and hold the same
// principal lock used by reset/disable. Acquire it before domain locks.
func (a *app) beginEmployeeCheckedWrite(r *http.Request, u user) (*sql.Tx, error) {
	return a.beginEmployeeCheckedWriteWithOptions(r, u, nil)
}

func (a *app) beginEmployeeCheckedWriteWithOptions(r *http.Request, u user, options *sql.TxOptions) (*sql.Tx, error) {
	tx, err := a.db.BeginTx(r.Context(), options)
	if err != nil {
		return nil, err
	}
	if !u.employee || safeMethod(r.Method) {
		return tx, nil
	}
	if !employeeLoginAllowed(r.Context()) {
		tx.Rollback()
		return nil, unauthenticated
	}
	entityID, err := pathID(r, "entity")
	if err != nil || entityID <= 0 {
		tx.Rollback()
		return nil, missingEntity
	}
	token := cookieValue(r, "sessionid")
	if len(token) != 32 {
		tx.Rollback()
		return nil, unauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var actor int64
	err = tx.QueryRowContext(r.Context(), `SELECT id FROM users WHERE id=$1 FOR UPDATE`, u.id).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		err = unauthenticated
	}
	if err == nil {
		// Read the credential after obtaining the row lock, so a reset
		// finishing while this transaction waited is observed.
		var current int64
		digest := sha256.Sum256([]byte(u.password))
		err = tx.QueryRowContext(r.Context(), `SELECT u.id FROM users u
   JOIN organization_employees c ON c.user_id=u.id
   JOIN memberships m ON (m.user_id,m.entity_id)=(c.user_id,c.entity_id)
   JOIN entities e ON e.id=c.entity_id
   JOIN sessions s ON s.user_id=u.id WHERE u.id=$1 AND u.active AND NOT u.staff AND NOT u.superuser
   AND c.entity_id=$2 AND c.status='active' AND m.active AND e.active
   AND s.token_hash=$3 AND s.expires>$4 AND s.password_digest=$5 AND s.auth_method='employee'
   AND u.password=$6 FOR SHARE OF s`, u.id, entityID, hash[:], time.Now().Unix(), digest[:], u.password).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			err = unauthenticated
		}
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
