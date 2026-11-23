package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	wikiProposalBrowser   = "/api/v1/entities/{entity}/outlets/{outlet}/wiki-create-proposals"
	wikiProposalAPI       = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/wiki-create-proposals"
	wikiProposalToken     = "/api/v1/auth/mcp-proposal-token"
	wikiProposalLifetime  = 15 * time.Minute
	wikiProposalRetention = 24 * time.Hour
)

var (
	wikiProposalMissing     = problem{404, "No se encontró la propuesta."}
	wikiProposalConflict    = problem{409, "La propuesta cambió o ya se resolvió. Volvé a cargarla."}
	wikiProposalUnavailable = problem{410, "La propuesta venció o se revocó su clave."}
	wikiProposalLimit       = problem{429, "Hay demasiadas claves o propuestas recientes. Esperá antes de volver a intentar."}
	wikiRequestID           = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
)

type wikiProposalScope struct {
	actor, entity, outlet int64
}

type wikiProposalReceipt struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	Expires   string `json:"expires_at"`
	ArticleID *int64 `json:"article_id,omitempty"`
}

type wikiProposalDetails struct {
	wikiProposalReceipt
	Title  *string `json:"title,omitempty"`
	Body   *string `json:"body,omitempty"`
	Digest string  `json:"digest,omitempty"`
}

type wikiProposalRow struct {
	wikiProposalDetails
	tokenID   int64
	expiry    int64
	rawDigest []byte
}

const wikiProposalColumns = `id,token_id,status,expires_at,article_id,title,body,digest`

func scanWikiProposal(row interface{ Scan(...any) error }) (wikiProposalRow, error) {
	var p wikiProposalRow
	err := row.Scan(&p.ID, &p.tokenID, &p.Status, &p.expiry, &p.ArticleID, &p.Title, &p.Body, &p.rawDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPWikiProposals(add func(string, string, http.HandlerFunc)) {
	if !a.enableMCPWikiProposals {
		return
	}
	add(wikiProposalToken, http.MethodPost, a.protectedWikiProposal(a.issueWikiProposalToken))
	add(wikiProposalToken+"/{token}", http.MethodDelete, a.protectedWikiProposal(a.revokeWikiProposalToken))
	add(wikiProposalBrowser, http.MethodGet, a.protectedWikiProposal(a.listWikiProposals))
	add(wikiProposalBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewWikiProposal))
	add(wikiProposalBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideWikiProposal))
	add(wikiProposalAPI, http.MethodPost, a.submitWikiProposal)
}

func (a *app) protectedWikiProposal(next func(http.ResponseWriter, *http.Request, user) error) http.HandlerFunc {
	return a.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
		if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" {
			return badInput
		}
		origin := "http://" + r.Host
		if effectiveHTTPS(r) {
			origin = "https://" + r.Host
		}
		origins := r.Header.Values("Origin")
		sites := r.Header.Values("Sec-Fetch-Site")
		if len(sites) > 0 && (len(sites) != 1 || sites[0] != "same-origin") {
			return badCSRF
		}
		if len(origins) > 0 && (len(origins) != 1 || origins[0] != origin) {
			return badCSRF
		}
		if safeMethod(r.Method) {
			if len(origins) == 0 && len(sites) == 0 {
				return badCSRF
			}
		} else if len(origins) != 1 {
			return badCSRF
		}
		return next(w, r, u)
	})
}

func wikiProposalPath(r *http.Request) (wikiProposalScope, error) {
	e, err := pathID(r, "entity")
	if err != nil || e <= 0 {
		return wikiProposalScope{}, missingEntity
	}
	o, err := pathID(r, "outlet")
	if err != nil || o <= 0 {
		return wikiProposalScope{}, missingOutlet
	}
	return wikiProposalScope{entity: e, outlet: o}, nil
}

// Lock the actor first on every mutation. Role, tenant and grant rows remain
// locked until the transaction ends so concurrent revocation cannot be missed.
func lockWikiProposalBrowser(r *http.Request, tx *sql.Tx, actor int64) ([]byte, error) {
	token := cookieValue(r, "sessionid")
	if len(token) != 32 {
		return nil, unauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var password string
	var digest []byte
	err := tx.QueryRowContext(r.Context(), `SELECT u.password,s.password_digest FROM users u
		JOIN sessions s ON s.user_id=u.id WHERE u.id=$1 AND u.active AND s.token_hash=$2 AND s.expires>$3
		FOR UPDATE OF u FOR SHARE OF s`, actor, hash[:], time.Now().Unix()).Scan(&password, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, unauthenticated
	}
	if err != nil {
		return nil, err
	}
	check := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(check[:], digest) != 1 {
		return nil, unauthenticated
	}
	return check[:], nil
}

func requireWikiProposalScope(ctx context.Context, tx *sql.Tx, s wikiProposalScope) error {
	return requireProposalScope(ctx, tx, s, wikiKeyPurpose)
}

const wikiKeyPurpose = "wiki"
const documentKeyPurpose = "document_draft"
const wikiEditKeyPurpose = "wiki_edit"
const stockReceiptKeyPurpose = "stock_receipt"

func requireProposalScope(ctx context.Context, tx *sql.Tx, s wikiProposalScope, purpose string) error {
	var membership int64
	var role string
	err := tx.QueryRowContext(ctx, `SELECT m.id,m.role FROM memberships m JOIN entities e ON e.id=m.entity_id AND e.active
		WHERE m.entity_id=$1 AND m.user_id=$2 AND m.active FOR SHARE OF m,e`, s.entity, s.actor).Scan(&membership, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return missingEntity
	}
	if err != nil {
		return err
	}
	allowed := (purpose == wikiKeyPurpose || purpose == wikiEditKeyPurpose) && role == "administration" && slices.Contains(capabilities[role], "manage_entity_access") ||
		purpose == documentKeyPurpose && slices.Contains(capabilities[role], draftCapability) ||
		purpose == stockReceiptKeyPurpose && hasStockReceiptRole(role)
	if !allowed {
		return forbiddenRole
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT o.id FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id AND g.membership_id=$1
		WHERE o.entity_id=$2 AND o.id=$3 AND o.active FOR SHARE OF o,g`, membership, s.entity, s.outlet).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return missingOutlet
	}
	return err
}

func (a *app) issueWikiProposalToken(w http.ResponseWriter, r *http.Request, u user) error {
	return a.issueProposalToken(w, r, u, wikiKeyPurpose)
}

func (a *app) issueProposalToken(w http.ResponseWriter, r *http.Request, u user, purpose string) error {
	raw, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	var input struct {
		Entity int64 `json:"entity_id"`
		Outlet int64 `json:"outlet_id"`
	}
	if err := decodeWikiProposalJSON(raw, &input, "entity_id", "outlet_id"); err != nil {
		return err
	}
	if input.Entity <= 0 || input.Outlet <= 0 {
		return badInput
	}
	s := wikiProposalScope{u.id, input.Entity, input.Outlet}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	password, err := lockWikiProposalBrowser(r, tx, u.id)
	if err != nil {
		return err
	}
	if err = requireProposalScope(r.Context(), tx, s, purpose); err != nil {
		return err
	}
	now := a.tokenTime()
	if err := checkProposalCredentialQuota(r.Context(), tx, u.id, purpose, now); err != nil {
		return err
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	hash := sha256.Sum256(secret)
	expiry := now.Add(wikiProposalLifetime)
	var id int64
	err = tx.QueryRowContext(r.Context(), `INSERT INTO mcp_wiki_proposal_tokens(actor_id,entity_id,outlet_id,token_hash,password_digest,issued_at,expires_at,purpose)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, s.actor, s.entity, s.outlet, hash[:], password, now.Unix(), expiry.Unix(), purpose).Scan(&id)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusCreated, map[string]any{"id": id, "token": base64.RawURLEncoding.EncodeToString(secret), "expires_at": expiry.UTC().Format(time.RFC3339)})
	return nil
}

func (a *app) revokeWikiProposalToken(w http.ResponseWriter, r *http.Request, u user) error {
	return a.revokeProposalToken(w, r, u, wikiKeyPurpose)
}

func (a *app) revokeProposalToken(w http.ResponseWriter, r *http.Request, u user, purpose string) error {
	id, err := pathID(r, "token")
	if err != nil || id <= 0 {
		return badInput
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	// An actor may revoke their own credential even if their role has changed.
	if _, err = tx.ExecContext(r.Context(), `UPDATE mcp_wiki_proposal_tokens SET revoked_at=$1
		WHERE id=$2 AND actor_id=$3 AND purpose=$4 AND revoked_at IS NULL`, now, id, u.id, purpose); err != nil {
		return err
	}
	switch purpose {
	case wikiKeyPurpose:
		_, err = tx.ExecContext(r.Context(), `UPDATE mcp_wiki_proposals SET status='revoked',title=NULL,body=NULL,decided_at=$1
			WHERE token_id=$2 AND actor_id=$3 AND status='pending'`, now, id, u.id)
	case documentKeyPurpose:
		_, err = tx.ExecContext(r.Context(), `UPDATE mcp_document_draft_proposals SET status='revoked',draft_preview=NULL,supplier_fingerprint=NULL,decided_at=$1
			WHERE token_id=$2 AND actor_id=$3 AND status='pending'`, now, id, u.id)
	case wikiEditKeyPurpose:
		_, err = tx.ExecContext(r.Context(), `UPDATE mcp_wiki_edit_proposals SET status='revoked',edit_preview=NULL,decided_at=$1
			WHERE token_id=$2 AND actor_id=$3 AND status='pending'`, now, id, u.id)
	case stockReceiptKeyPurpose:
		_, err = tx.ExecContext(r.Context(), `UPDATE mcp_stock_receipt_proposals SET status='revoked',receipt_preview=NULL,decided_at=$1
			WHERE token_id=$2 AND actor_id=$3 AND status='pending'`, now, id, u.id)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func wikiProposalDigest(s wikiProposalScope, title, body string) [32]byte {
	// Type and scope are part of the canonical digest. The fixed field order,
	// JSON string escaping, and version prefix make the boundaries unambiguous.
	raw, _ := json.Marshal(struct {
		Kind   string `json:"kind"`
		Actor  int64  `json:"actor_id"`
		Entity int64  `json:"entity_id"`
		Outlet int64  `json:"outlet_id"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}{"wiki_create.v1", s.actor, s.entity, s.outlet, title, body})
	return sha256.Sum256(raw)
}

func normalizeWikiProposal(title, body string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" || strings.TrimSpace(body) == "" || !utf8.ValidString(title) || !utf8.ValidString(body) ||
		utf8.RuneCountInString(title) > 120 || utf8.RuneCountInString(body) > 12000 || len(body) > 48<<10 ||
		strings.ContainsRune(title, 0) || strings.ContainsRune(body, 0) {
		return "", badInput
	}
	return title, nil
}

func decodeWikiProposalJSON(raw []byte, dst any, fields ...string) error {
	if err := decodePayables(raw, dst, fields...); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return badInput
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		k, ok := key.(string)
		if err != nil || !ok || seen[k] {
			return badInput
		}
		seen[k] = true
		var v json.RawMessage
		if err = d.Decode(&v); err != nil {
			return badInput
		}
	}
	if len(seen) != len(fields) {
		return badInput
	}
	return nil
}

func (a *app) submitWikiProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptWikiProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptWikiProposal(w http.ResponseWriter, r *http.Request) error {
	if r.URL.RawQuery != "" || len(r.Header.Values("Cookie")) != 0 {
		return badInput
	}
	hash, err := parseReadBearer(r)
	if err != nil {
		return err
	}
	scope, err := wikiProposalPath(r)
	if err != nil {
		return err
	}
	raw, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	var input struct {
		RequestID string `json:"request_id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
	}
	if err = decodeWikiProposalJSON(raw, &input, "request_id", "title", "body"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(input.RequestID) {
		return badInput
	}
	title, err := normalizeWikiProposal(input.Title, input.Body)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tokenID, expires, err := a.loadProposalKey(ctx, tx, hash, &scope, wikiKeyPurpose)
	if err != nil {
		return err
	}
	if err = requireWikiProposalScope(ctx, tx, scope); err != nil {
		return err
	}
	now := a.tokenTime()

	digest := wikiProposalDigest(scope, title, input.Body)
	p, err := scanWikiProposal(tx.QueryRowContext(ctx, `SELECT `+wikiProposalColumns+` FROM mcp_wiki_proposals
		WHERE actor_id=$1 AND entity_id=$2 AND outlet_id=$3 AND request_id=$4 FOR UPDATE`,
		scope.actor, scope.entity, scope.outlet, input.RequestID))
	status := http.StatusOK
	if err == nil {
		if subtle.ConstantTimeCompare(p.rawDigest, digest[:]) != 1 {
			return wikiProposalConflict
		}
		if p.Status == "pending" && p.expiry <= now.Unix() {
			p.Status = "expired"
		}
	} else if err == wikiProposalMissing {
		var pending, recent int64
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE expires_at>$2 AND status='pending'),COUNT(*)
			FROM mcp_wiki_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		now = a.tokenTime()
		if now.Unix() >= expires {
			return unauthenticated
		}
		p, err = scanWikiProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_wiki_proposals
			(token_id,actor_id,entity_id,outlet_id,request_id,digest,title,body,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+wikiProposalColumns,
			tokenID, scope.actor, scope.entity, scope.outlet, input.RequestID, digest[:], title, input.Body, now.Unix(), expires))
		if err != nil {
			return err
		}
		status = http.StatusCreated
	} else {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	p.ArticleID = nil // The proposal credential never accesses the published article.
	jsonResponse(w, status, p.wikiProposalReceipt)
	return nil
}

// Lock the hinted actor and then re-read credentials so revocation and
// rotation cannot race a successful authorization transaction.
func (a *app) loadProposalKey(ctx context.Context, tx *sql.Tx, hash []byte, scope *wikiProposalScope, purpose string) (tokenID, expires int64, err error) {
	var password string
	var entity, outlet int64
	var tokenPassword []byte
	err = tx.QueryRowContext(ctx, `SELECT actor_id FROM mcp_wiki_proposal_tokens WHERE token_hash=$1 AND purpose=$2`, hash, purpose).Scan(&scope.actor)
	if errors.Is(err, sql.ErrNoRows) {
		err = unauthenticated
	}
	if err != nil {
		return
	}
	err = tx.QueryRowContext(ctx, `SELECT password FROM users WHERE id=$1 AND active FOR UPDATE`, scope.actor).Scan(&password)
	if errors.Is(err, sql.ErrNoRows) {
		err = unauthenticated
	}
	if err != nil {
		return
	}
	now := a.tokenTime().Unix()
	err = tx.QueryRowContext(ctx, `SELECT id,entity_id,outlet_id,expires_at,password_digest FROM mcp_wiki_proposal_tokens
		WHERE token_hash=$1 AND actor_id=$2 AND revoked_at IS NULL AND issued_at<=$3 AND expires_at>$3 AND purpose=$4 FOR SHARE`,
		hash, scope.actor, now, purpose).Scan(&tokenID, &entity, &outlet, &expires, &tokenPassword)
	if errors.Is(err, sql.ErrNoRows) {
		err = unauthenticated
	}
	if err != nil {
		return
	}
	digestPassword := sha256.Sum256([]byte(password))
	switch {
	case subtle.ConstantTimeCompare(tokenPassword, digestPassword[:]) != 1:
		err = unauthenticated
	case entity != scope.entity:
		err = missingEntity
	case outlet != scope.outlet:
		err = missingOutlet
	}
	return
}

func (a *app) beginWikiReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, []byte, error) {
	scope, err := wikiProposalPath(r)
	if err != nil {
		return nil, scope, nil, err
	}
	scope.actor = u.id
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil, scope, nil, err
	}
	password, err := lockWikiProposalBrowser(r, tx, u.id)
	if err == nil {
		err = requireWikiProposalScope(r.Context(), tx, scope)
	}
	if err != nil {
		tx.Rollback()
		return nil, scope, nil, err
	}
	return tx, scope, password, nil
}

type wikiProposalListItem struct {
	wikiProposalReceipt
	Title *string `json:"title,omitempty"`
}

func (a *app) listWikiProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, password, err := a.beginWikiReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND (t.revoked_at IS NOT NULL OR t.password_digest<>$5) THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.article_id,
		CASE WHEN p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL AND t.password_digest=$5 THEN p.title END
		FROM mcp_wiki_proposals p JOIN mcp_wiki_proposal_tokens t ON t.id=p.token_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL AND t.password_digest=$5) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, a.tokenTime().Unix(), password, a.tokenTime().Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []wikiProposalListItem{}
	for rows.Next() {
		var item wikiProposalListItem
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.ArticleID, &item.Title); err != nil {
			return err
		}
		item.Expires = time.Unix(expiry, 0).UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, map[string]any{"proposals": items})
	return nil
}

func lookupWikiProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope) (wikiProposalRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return wikiProposalRow{}, wikiProposalMissing
	}
	return scanWikiProposal(tx.QueryRowContext(r.Context(), `SELECT `+wikiProposalColumns+` FROM mcp_wiki_proposals
		WHERE id=$1 AND actor_id=$2 AND entity_id=$3 AND outlet_id=$4 FOR UPDATE`, id, s.actor, s.entity, s.outlet))
}

func wikiProposalTokenStatus(ctx context.Context, tx *sql.Tx, token int64, password []byte) (bool, error) {
	var tokenPassword []byte
	var revoked sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT password_digest,revoked_at FROM mcp_wiki_proposal_tokens
		WHERE id=$1 FOR SHARE`, token).Scan(&tokenPassword, &revoked)
	if err != nil {
		return false, err
	}
	return !revoked.Valid && subtle.ConstantTimeCompare(password, tokenPassword) == 1, nil
}

func (a *app) viewWikiProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, password, err := a.beginWikiReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupWikiProposal(r, tx, s)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	if p.Status == "pending" {
		valid, err := wikiProposalTokenStatus(r.Context(), tx, p.tokenID, password)
		if err != nil {
			return err
		}
		switch {
		case p.expiry <= a.tokenTime().Unix():
			p.Status = "expired"
		case !valid:
			p.Status = "revoked"
		default:
			p.Digest = hex.EncodeToString(p.rawDigest)
		}
	}
	if p.Status != "pending" {
		p.Title, p.Body = nil, nil
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.wikiProposalDetails)
	return nil
}

func setWikiProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, article *int64) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_wiki_proposals SET status=$1,title=NULL,body=NULL,
		decided_at=$2,article_id=$3 WHERE id=$4 AND status='pending'`, status, now, article, id))
}

func (a *app) decideWikiProposal(w http.ResponseWriter, r *http.Request, u user) error {
	raw, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	var input struct {
		Decision string `json:"decision"`
		Digest   string `json:"digest"`
		Confirm  bool   `json:"confirm"`
	}
	if err = decodeWikiProposalJSON(raw, &input, "decision", "digest", "confirm"); err != nil {
		return err
	}
	if !input.Confirm || (input.Decision != "approve" && input.Decision != "reject") || len(input.Digest) != 64 ||
		input.Digest != strings.ToLower(input.Digest) {
		return badInput
	}
	digest, err := hex.DecodeString(input.Digest)
	if err != nil {
		return badInput
	}
	tx, s, password, err := a.beginWikiReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupWikiProposal(r, tx, s)
	if err != nil {
		return err
	}
	switch p.Status {
	case "expired", "revoked":
		return wikiProposalUnavailable
	case "approved", "rejected":
		return wikiProposalConflict
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	if p.Title == nil || p.Body == nil {
		return wikiProposalConflict
	}
	valid, err := wikiProposalTokenStatus(r.Context(), tx, p.tokenID, password)
	if err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if !valid || p.expiry <= now {
		state := "revoked"
		if p.expiry <= now {
			state = "expired"
		}
		if err = setWikiProposalTerminal(r.Context(), tx, p.ID, state, now, nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	title, err := normalizeWikiProposal(*p.Title, *p.Body)
	if err != nil {
		return wikiProposalConflict
	}
	canonical := wikiProposalDigest(s, title, *p.Body)
	if subtle.ConstantTimeCompare(p.rawDigest, digest) != 1 || subtle.ConstantTimeCompare(p.rawDigest, canonical[:]) != 1 {
		return wikiProposalConflict
	}

	p.Status = "rejected"
	if input.Decision == "approve" {
		m, err := scopedMembership(tx, s.actor, s.entity, "manage_entity_access")
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]string{"titulo": title, "contenido": *p.Body})
		if err != nil {
			return err
		}
		_, v, err := writeKnowledge(r, tx, m, payload)
		if err != nil {
			return err
		}
		article := v.(knowledgeArticle)
		p.ArticleID = &article.ID
		p.Status = "approved"
	}
	now = a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if _, err = lockWikiProposalBrowser(r, tx, s.actor); err != nil {
		return err
	}
	if err = setWikiProposalTerminal(r.Context(), tx, p.ID, p.Status, now, p.ArticleID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.wikiProposalReceipt)
	return nil
}

func (a *app) pruneWikiProposals(ctx context.Context) error {
	now := a.tokenTime()
	// Lock proposal rows only, never an actor. Skip decisions already in
	// flight; the deciding transaction independently checks its deadline.
	_, err := a.db.ExecContext(ctx, `WITH expired AS (
		SELECT id FROM mcp_wiki_proposals WHERE status='pending' AND expires_at<=$1
		ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED
	) UPDATE mcp_wiki_proposals p SET status='expired',title=NULL,body=NULL,decided_at=$1
		FROM expired WHERE p.id=expired.id`, now.Unix())
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `WITH expired AS (
		SELECT id FROM mcp_document_draft_proposals WHERE status='pending' AND expires_at<=$1
		ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED
	) UPDATE mcp_document_draft_proposals p SET status='expired',draft_preview=NULL,supplier_fingerprint=NULL,decided_at=$1
		FROM expired WHERE p.id=expired.id`, now.Unix())
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `WITH expired AS (
		SELECT id FROM mcp_wiki_edit_proposals WHERE status='pending' AND expires_at<=$1
		ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED
	) UPDATE mcp_wiki_edit_proposals p SET status='expired',edit_preview=NULL,decided_at=$1
		FROM expired WHERE p.id=expired.id`, now.Unix())
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `WITH expired AS (
		SELECT id FROM mcp_stock_receipt_proposals WHERE status='pending' AND expires_at<=$1
		ORDER BY expires_at,id LIMIT 100 FOR UPDATE SKIP LOCKED
	) UPDATE mcp_stock_receipt_proposals p SET status='expired',receipt_preview=NULL,decided_at=$1
		FROM expired WHERE p.id=expired.id`, now.Unix())
	if err != nil {
		return err
	}
	if err := a.pruneReconciliationDraftProposals(ctx, now); err != nil {
		return err
	}
	if err := a.pruneSupplierNameProposals(ctx, now); err != nil {
		return err
	}
	if err := a.pruneStockTrackingProposals(ctx, now); err != nil {
		return err
	}
	if err := a.pruneStockMinimumProposals(ctx, now); err != nil {
		return err
	}
	if err := a.prunePOSCreateProposals(ctx, now); err != nil {
		return err
	}
	if err := a.pruneReservationCancelProposals(ctx, now); err != nil {
		return err
	}
	if err := a.pruneReservationConfirmProposals(ctx, now); err != nil {
		return err
	}
	if err := a.prunePOSCatalogProposals(ctx, now); err != nil {
		return err
	}
	if err := a.pruneStockAdjustmentProposals(ctx, now); err != nil {
		return err
	}
	if err := a.pruneDocumentEditProposals(ctx, now); err != nil {
		return err
	}
	if err := a.pruneDocumentReviewProposals(ctx, now); err != nil {
		return err
	}
	if err := a.prunePOSRenameProposals(ctx, now); err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `WITH old AS (
		SELECT t.id FROM mcp_wiki_proposal_tokens t WHERE t.expires_at<=$1
		ORDER BY t.expires_at,t.id LIMIT 100 FOR UPDATE SKIP LOCKED
	) DELETE FROM mcp_wiki_proposal_tokens t USING old WHERE t.id=old.id`, now.Add(-wikiProposalRetention).Unix())
	return err
}

func (a *app) pruneWikiProposalsLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := a.pruneWikiProposals(pass); err != nil && ctx.Err() == nil {
			log.Print("could not prune wiki proposals")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
