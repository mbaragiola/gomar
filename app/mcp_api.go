package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const mcpReadCapability = "read_business_data"
const mcpTokenLifetime = 15 * time.Minute

type mcpTokenScope struct {
	entityMembership
	outlet   sql.NullInt64
	username string
}

type mcpPlace struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type mcpSalesOutlet struct {
	mcpPlace
	SaleCount  int64 `json:"sale_count"`
	TotalMinor int64 `json:"total_minor"`
}

type mcpSales struct {
	Period     string           `json:"period"`
	Timezone   string           `json:"timezone"`
	From       string           `json:"from"`
	To         string           `json:"to"`
	Currency   string           `json:"currency"`
	SaleCount  int64            `json:"sale_count"`
	TotalMinor int64            `json:"total_minor"`
	Outlets    []mcpSalesOutlet `json:"outlets"`
}

func (a *app) tokenTime() time.Time {
	if a.mcpClock != nil {
		return a.mcpClock()
	}
	return time.Now()
}

func (a *app) registerMCP(add func(string, string, http.HandlerFunc)) {
	if !a.enableMCP {
		return
	}
	add("/api/v1/auth/mcp-token", http.MethodPost, a.protected(a.issueMCPToken))
	add("/api/v1/auth/mcp-token/{token}", http.MethodDelete, a.protected(a.revokeMCPToken))
	add("/api/v1/mcp/me", http.MethodGet, a.mcpRead(mcpMe))
	add("/api/v1/mcp/entities", http.MethodGet, a.mcpRead(mcpEntities))
	add("/api/v1/mcp/entities/{entity}/outlets", http.MethodGet, a.mcpRead(mcpOutlets))
	if a.enablePOS {
		add("/api/v1/mcp/entities/{entity}/sales-summary", http.MethodGet, a.mcpRead(a.mcpSalesSummary))
	}
}

func (a *app) issueMCPToken(w http.ResponseWriter, r *http.Request, u user) error {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		Entity int64  `json:"entity_id"`
		Outlet *int64 `json:"outlet_id"`
	}
	body, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	if err := decodePayables(body, &input, "entity_id", "outlet_id"); err != nil {
		return err
	}
	if input.Entity <= 0 || input.Outlet != nil && *input.Outlet <= 0 {
		return badInput
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := scopedMembership(tx, u.id, input.Entity, mcpReadCapability)
	if err != nil {
		return err
	}
	if input.Outlet != nil {
		if err := requireSelectedDocumentOutlet(tx, m, *input.Outlet); err != nil {
			return err
		}
	}
	now := a.tokenTime()
	expiry := now.Add(mcpTokenLifetime)
	raw := make([]byte, 32)
	rand.Read(raw)
	sum := sha256.Sum256(raw)
	password := sha256.Sum256([]byte(u.password))
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO mcp_read_tokens(user_id,entity_id,outlet_id,token_hash,password_digest,issued_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, u.id, input.Entity, input.Outlet, sum[:], password[:], now.Unix(), expiry.Unix()).Scan(&id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusCreated, map[string]any{"id": id, "token": base64.RawURLEncoding.EncodeToString(raw), "expires_at": expiry.UTC().Format(time.RFC3339)})
	return nil
}

func (a *app) revokeMCPToken(w http.ResponseWriter, r *http.Request, u user) error {
	w.Header().Set("Cache-Control", "no-store")
	id, err := pathID(r, "token")
	if err != nil || id <= 0 {
		return badInput
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM mcp_read_tokens WHERE id=$1 AND user_id=$2`, id, u.id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func parseReadBearer(r *http.Request) ([]byte, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return nil, unauthenticated
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(token) != 43 {
		return nil, unauthenticated
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, unauthenticated
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func loadMCPToken(ctx context.Context, tx *sql.Tx, hash []byte, now time.Time) (mcpTokenScope, error) {
	s := mcpTokenScope{}
	var entityID, userID int64
	var digest []byte
	var password string
	err := tx.QueryRowContext(ctx, `SELECT t.user_id,t.entity_id,t.outlet_id,t.password_digest,u.username,u.password
		FROM mcp_read_tokens t JOIN users u ON u.id=t.user_id AND u.active=TRUE
		WHERE t.token_hash=$1 AND t.issued_at<=$2 AND t.expires_at>$2`, hash, now.Unix()).
		Scan(&userID, &entityID, &s.outlet, &digest, &s.username, &password)
	if errors.Is(err, sql.ErrNoRows) {
		return s, unauthenticated
	}
	if err != nil {
		return s, err
	}
	check := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(check[:], digest) != 1 {
		return s, unauthenticated
	}
	s.entityMembership, err = scopedMembership(tx, userID, entityID, mcpReadCapability)
	if err != nil {
		return s, err
	}
	if s.outlet.Valid {
		err = requireSelectedDocumentOutlet(tx, s.entityMembership, s.outlet.Int64)
	}
	return s, err
}

type mcpReadHandler func(*http.Request, *sql.Tx, mcpTokenScope) (any, error)

func (a *app) mcpRead(next mcpReadHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		hash, err := parseReadBearer(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			failure(w, err)
			return
		}
		tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			failure(w, err)
			return
		}
		defer tx.Rollback()
		s, err := loadMCPToken(r.Context(), tx, hash, a.tokenTime())
		if err != nil {
			failure(w, err)
			return
		}
		if entity := r.PathValue("entity"); entity != "" {
			id, e := pathID(r, "entity")
			if e != nil || id != s.ID {
				failure(w, missingEntity)
				return
			}
		}
		result, err := next(r, tx, s)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			failure(w, err)
			return
		}
		jsonResponse(w, http.StatusOK, result)
	}
}

func mcpMe(r *http.Request, _ *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	return map[string]any{"id": s.userID, "username": s.username}, nil
}

func mcpEntities(r *http.Request, _ *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	return map[string]any{"entities": []mcpPlace{{s.ID, s.Name}}}, nil
}

func mcpOutlets(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, badInput
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT o.id,o.name FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id
		WHERE g.membership_id=$1 AND o.entity_id=$2 AND o.active=TRUE AND ($3::bigint IS NULL OR o.id=$3)
		ORDER BY o.id`, s.id, s.ID, s.outlet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mcpPlace{}
	for rows.Next() {
		var v mcpPlace
		if err := rows.Scan(&v.ID, &v.Name); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return map[string]any{"outlets": values}, rows.Err()
}

func mcpMonth(now time.Time, period string) (time.Time, time.Time, error) {
	local := now.In(payablesLocation)
	from := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, payablesLocation)
	switch period {
	case "this_month":
	case "previous_month":
		from = from.AddDate(0, -1, 0)
	default:
		return time.Time{}, time.Time{}, badInput
	}
	return from, from.AddDate(0, 1, 0), nil
}

func (a *app) mcpSalesSummary(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["period"]) != 1 {
		return nil, badInput
	}
	for key, values := range q {
		if key != "period" && key != "outlet_id" || len(values) != 1 {
			return nil, badInput
		}
	}
	outlet := s.outlet
	if requested := q["outlet_id"]; len(requested) == 1 {
		id, err := strconv.ParseInt(requested[0], 10, 64)
		if err != nil || id <= 0 {
			return nil, badInput
		}
		if outlet.Valid && id != outlet.Int64 {
			return nil, missingOutlet
		}
		if err := requireSelectedDocumentOutlet(tx, s.entityMembership, id); err != nil {
			return nil, err
		}
		outlet = sql.NullInt64{Int64: id, Valid: true}
	}
	period := q.Get("period")
	from, to, err := mcpMonth(a.tokenTime(), period)
	if err != nil {
		return nil, err
	}
	// The stored UTC string can end immediately in Z or have fractional
	// seconds. Whole-second prefixes keep both forms on the correct side.
	const prefix = "2006-01-02T15:04:05"
	rows, err := tx.QueryContext(r.Context(), `SELECT o.id,o.name,count(p.id),COALESCE(sum(p.total_minor),0)::bigint
		FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id
		LEFT JOIN pos_sales p ON p.outlet_id=o.id AND p.recorded_at >= $4 AND p.recorded_at < $5
		WHERE g.membership_id=$1 AND o.entity_id=$2 AND o.active=TRUE AND ($3::bigint IS NULL OR o.id=$3)
		GROUP BY o.id,o.name ORDER BY o.id`, s.id, s.ID, outlet, from.UTC().Format(prefix), to.UTC().Format(prefix))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := mcpSales{Period: period, Timezone: payablesLocation.String(), From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Currency: "ARS", Outlets: []mcpSalesOutlet{}}
	for rows.Next() {
		var row mcpSalesOutlet
		if err := rows.Scan(&row.ID, &row.Name, &row.SaleCount, &row.TotalMinor); err != nil {
			return nil, err
		}
		if res.SaleCount > (1<<63)-1-row.SaleCount || res.TotalMinor > (1<<63)-1-row.TotalMinor {
			return nil, errors.New("sales summary exceeds signed 64-bit range")
		}
		res.SaleCount += row.SaleCount
		res.TotalMinor += row.TotalMinor
		res.Outlets = append(res.Outlets, row)
	}
	return res, rows.Err()
}
