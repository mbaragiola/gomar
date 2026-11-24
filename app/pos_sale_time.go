package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mbaragiola/gomar/app/internal/postime"
)

var posSaleTimeMissing = problem{404, "No se encontró la referencia de venta de Caja."}

func (a *app) posTime() time.Time {
	if a.posClock != nil {
		return a.posClock()
	}
	return time.Now()
}

func parsePOSSaleTillTime(raw json.RawMessage) (string, error) {
	if raw == nil || string(raw) == "null" {
		return "", nil
	}
	var claim string
	if err := json.Unmarshal(raw, &claim); err != nil {
		return "", badInput
	}
	if _, ok := postime.ParseUTC(claim); !ok {
		return "", badInput
	}
	return claim, nil
}

func readPOSSaleTime(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	if !posSellingRole(m.role) {
		return 0, nil, forbiddenRole
	}
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	result, err := loadPOSSaleTime(r, tx, m, outlet)
	return http.StatusOK, result, err
}

func mcpPOSSaleTime(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if !posSellingRole(s.role) {
		return nil, forbiddenRole
	}
	if !s.outlet.Valid {
		return nil, missingOutlet
	}
	outlet, err := mcpWorkspaceOutlet(r, tx, s)
	if err != nil {
		return nil, err
	}
	return loadPOSSaleTime(r, tx, s.entityMembership, outlet)
}

func loadPOSSaleTime(r *http.Request, tx *sql.Tx, m entityMembership, outlet int64) (postime.Evidence, error) {
	request := r.PathValue("request")
	if !postime.ValidRequestID(request) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return postime.Evidence{}, badInput
	}
	result := postime.Evidence{RequestID: request}
	err := tx.QueryRowContext(r.Context(), `SELECT s.recorded_at,COALESCE(s.till_reported_at_utc,'') FROM pos_sales s
		WHERE s.request_id=$1 AND s.outlet_id=$2 AND s.actor_id=$3
		AND EXISTS(SELECT 1 FROM outlet_grants g
			JOIN memberships member ON member.id=g.membership_id
			JOIN outlets o ON o.id=g.outlet_id
			JOIN entities e ON e.id=o.entity_id
			WHERE g.outlet_id=s.outlet_id AND member.id=$4 AND member.user_id=s.actor_id
			AND member.entity_id=e.id AND e.id=$5 AND member.role=$6 AND member.active AND o.active AND e.active)
		AND NOT EXISTS(SELECT 1 FROM pos_table_finalizations f WHERE f.sale_id=s.id)`, request, outlet, m.userID, m.id, m.ID, m.role).
		Scan(&result.RegisteredAt, &result.TillReportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return postime.Evidence{}, posSaleTimeMissing
	}
	if err != nil {
		return postime.Evidence{}, err
	}
	registered, ok := postime.ParseUTC(result.RegisteredAt)
	if !ok {
		return postime.Evidence{}, errors.New("invalid registered sale time")
	}
	result.TillTimeStatus, ok = postime.Status(result.TillReportedAt, registered)
	if !ok {
		return postime.Evidence{}, errors.New("invalid till-reported sale time")
	}
	result.HandoffURL = postime.Handoff(m.ID, outlet, request)
	return result, nil
}
