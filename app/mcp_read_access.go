package main

import (
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const readAccessPageSize = 20

type ownMCPReadAccess struct {
	ID        string `json:"id"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
}

func ownReadAccessCursor(r *http.Request) (int64, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || r.URL.ForceQuery {
		return 0, badInput
	}
	if len(q) == 0 {
		return 0, nil
	}
	if len(q) != 1 || len(q["before_id"]) != 1 {
		return 0, badInput
	}
	value := q.Get("before_id")
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
		return 0, badInput
	}
	return id, nil
}

// Only the signed-in owner can rediscover revocation handles, not secrets.
// Broad keys and proposal credentials are outside this browser panel.
func (a *app) listOwnMCPReadAccess(w http.ResponseWriter, r *http.Request, u user) error {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if len(r.Header.Values("Authorization")) != 0 || r.URL.RawPath != "" {
		return badInput
	}
	entity, entityOK := attachmentHandoffID(r, "entity")
	outlet, outletOK := attachmentHandoffID(r, "outlet")
	if !entityOK || !outletOK {
		return missingOutlet
	}
	before, err := ownReadAccessCursor(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entity, mcpReadCapability)
	if err != nil {
		return err
	}
	if err := requireSelectedDocumentOutlet(tx, member, outlet); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	digest := sha256.Sum256([]byte(u.password))
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.issued_at,t.expires_at FROM mcp_read_tokens t
		JOIN users u ON u.id=t.user_id AND u.active=TRUE AND u.password=$6
		WHERE t.user_id=$1 AND t.entity_id=$2 AND t.outlet_id=$3 AND ($4::bigint=0 OR t.id<$4)
			AND t.issued_at<=$5 AND t.expires_at>$5 AND t.password_digest=$7
		ORDER BY t.id DESC LIMIT $8`, u.id, entity, outlet, before, now, u.password, digest[:], readAccessPageSize+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	page := struct {
		Access   []ownMCPReadAccess `json:"access"`
		BeforeID string             `json:"before_id,omitempty"`
	}{Access: []ownMCPReadAccess{}}
	for rows.Next() {
		var id, issued, expires int64
		if err := rows.Scan(&id, &issued, &expires); err != nil {
			return err
		}
		page.Access = append(page.Access, ownMCPReadAccess{
			ID: strconv.FormatInt(id, 10), IssuedAt: time.Unix(issued, 0).UTC().Format(time.RFC3339),
			ExpiresAt: time.Unix(expires, 0).UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(page.Access) > readAccessPageSize {
		page.Access = page.Access[:readAccessPageSize]
		page.BeforeID = page.Access[readAccessPageSize-1].ID
	}
	jsonResponse(w, http.StatusOK, page)
	return nil
}
