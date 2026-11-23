package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const outletDisplayLabelSessionAction = "outlet_display_label_change"

func (a *app) outletDisplayLabelProposalsEnabled() bool {
	return a.enableMCPOutletDisplayLabelProposals
}

const outletDisplayLabelWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/outlet-display-label-proposals"
const outletDisplayLabelWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/outlet-display-label-proposals"

type outletDisplayLabelWireReceipt struct {
	ID      int64   `json:"id"`
	Status  string  `json:"status"`
	Expires string  `json:"expires_at"`
	Name    *string `json:"name,omitempty"`
}
type outletDisplayLabelWireDetails struct {
	outletDisplayLabelWireReceipt
	LabelChange *outletDisplayLabelPreview `json:"label_change,omitempty"`
	Digest      string                     `json:"digest,omitempty"`
}
type outletDisplayLabelWireRow struct {
	outletDisplayLabelWireReceipt
	sessionID, expiry                 int64
	inputDigest, labelDigest, preview []byte
}

const outletDisplayLabelWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.approved_name,p.input_digest,p.label_digest,p.label_preview`

func scanOutletDisplayLabelProposal(row interface{ Scan(...any) error }) (outletDisplayLabelWireRow, error) {
	var p outletDisplayLabelWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.Name, &p.inputDigest, &p.labelDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPOutletDisplayLabelProposals(add func(string, string, http.HandlerFunc)) {
	if !a.outletDisplayLabelProposalsEnabled() {
		return
	}
	add(outletDisplayLabelWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listOutletDisplayLabelProposals))
	add(outletDisplayLabelWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewOutletDisplayLabelProposal))
	add(outletDisplayLabelWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideOutletDisplayLabelProposal))
	add(outletDisplayLabelWireAPI, http.MethodPost, a.submitOutletDisplayLabelProposal)
}

func outletDisplayLabelWireInputDigest(s reservationConfirmScope, input outletDisplayLabelInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope reservationConfirmScope
		Input outletDisplayLabelInput
	}{outletDisplayLabelSessionAction, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitOutletDisplayLabelProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptOutletDisplayLabelProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptOutletDisplayLabelProposal(w http.ResponseWriter, r *http.Request) error {
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
	if len(raw) > 1280 {
		return badInput
	}
	var body struct {
		RequestID   string          `json:"request_id"`
		LabelChange json.RawMessage `json:"label_change"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "label_change"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in, err := decodeOutletDisplayLabel(body.LabelChange)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadNamedProposalSession(ctx, tx, hash, &scope, outletDisplayLabelSessionAction)
	if err != nil {
		return err
	}
	s := reservationConfirmScopeFrom(scope)
	if err = a.lockOutletDisplayLabelScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := outletDisplayLabelWireInputDigest(s, in)
	p, err := scanOutletDisplayLabelProposal(tx.QueryRowContext(ctx, `SELECT `+outletDisplayLabelWireColumns+` FROM mcp_outlet_display_label_proposals p
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND p.request_id=$4 FOR UPDATE OF p`,
		scope.actor, scope.entity, scope.outlet, body.RequestID))
	now := a.tokenTime()
	if now.Unix() >= expiry {
		return unauthenticated
	}
	status := http.StatusOK
	switch err {
	case nil:
		// A matching request belongs to its original browser login. A later
		// login cannot adopt that proposal by reusing the request ID.
		var sameLogin bool
		err = tx.QueryRowContext(ctx, `SELECT first.login_hash=new.login_hash FROM mcp_proposal_sessions first JOIN mcp_proposal_sessions new ON new.id=$2
			WHERE first.id=$1`, p.sessionID, session).Scan(&sameLogin)
		if err != nil {
			return err
		}
		if !sameLogin {
			return wikiProposalConflict
		}
		if subtle.ConstantTimeCompare(p.inputDigest, inputDigest[:]) != 1 {
			return wikiProposalConflict
		}
		if p.Status == "pending" && p.expiry <= now.Unix() {
			p.Status = "expired"
		}
	case wikiProposalMissing:
		var pending, recent int64
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE expires_at>$2 AND status='pending'),COUNT(*)
			FROM mcp_outlet_display_label_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := readOutletDisplayLabel(ctx, tx, s, in)
		if err != nil {
			return err
		}
		labelDigest := prepared.digest()
		preview, err := json.Marshal(prepared)
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}

		p, err = scanOutletDisplayLabelProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_outlet_display_label_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,request_id,input_digest,label_digest,
			label_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+outletDisplayLabelWireColumns,
			session, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], labelDigest[:], preview, now.Unix(), expiry))
		if err != nil {
			return err
		}
		status = http.StatusCreated
	default:
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	p.Name = nil
	jsonResponse(w, status, p.outletDisplayLabelWireReceipt)
	return nil
}

func (a *app) listOutletDisplayLabelProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginOutletDisplayLabelReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.approved_name
		FROM mcp_outlet_display_label_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []outletDisplayLabelWireReceipt{}
	for rows.Next() {
		var item outletDisplayLabelWireReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.Name); err != nil {
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

func lookupOutletDisplayLabelProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte) (outletDisplayLabelWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return outletDisplayLabelWireRow{}, wikiProposalMissing
	}
	return scanOutletDisplayLabelProposal(tx.QueryRowContext(r.Context(), `SELECT `+outletDisplayLabelWireColumns+` FROM mcp_outlet_display_label_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:]))
}

func (p outletDisplayLabelWireRow) validatedOutletDisplayLabel(s wikiProposalScope) (outletDisplayLabelPreview, error) {
	var preview outletDisplayLabelPreview
	if len(p.labelDigest) != 32 || json.Unmarshal(p.preview, &preview) != nil {
		return preview, outletDisplayLabelChanged
	}
	preview.Scope = reservationConfirmScopeFrom(s)
	got := preview.digest()
	if subtle.ConstantTimeCompare(got[:], p.labelDigest) != 1 {
		return preview, outletDisplayLabelChanged
	}
	return preview, nil
}

func (a *app) viewOutletDisplayLabelProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginOutletDisplayLabelReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupOutletDisplayLabelProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.outletDisplayLabelProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := outletDisplayLabelWireDetails{outletDisplayLabelWireReceipt: p.outletDisplayLabelWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedOutletDisplayLabel(s)
		if err != nil {
			return err
		}
		result.LabelChange = &draft
		result.Digest = hex.EncodeToString(p.labelDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setOutletDisplayLabelProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, name *string) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_outlet_display_label_proposals SET status=$1,label_preview=NULL,
		decided_at=$2,approved_name=$3 WHERE id=$4 AND status='pending'`, status, now, name, id))
}

func (a *app) decideOutletDisplayLabelProposal(w http.ResponseWriter, r *http.Request, u user) error {
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
	var confirmed [32]byte
	if _, err = hex.Decode(confirmed[:], []byte(input.Digest)); err != nil {
		return badInput
	}
	tx, s, login, err := a.beginOutletDisplayLabelReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupOutletDisplayLabelProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	switch p.Status {
	case "approved", "rejected":
		return wikiProposalConflict
	case "expired", "revoked":
		return wikiProposalUnavailable
	}
	state, err := a.outletDisplayLabelProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setOutletDisplayLabelProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.labelDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedOutletDisplayLabel(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		if err := a.applyOutletDisplayLabel(r, tx, u, reservationConfirmScopeFrom(s), draft, confirmed); err != nil {
			return err
		}
		p.Name = &draft.Name
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setOutletDisplayLabelProposalTerminal(ctx, tx, p.ID, p.Status, now, p.Name); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.outletDisplayLabelWireReceipt)
	return nil
}

func (a *app) outletDisplayLabelProposalState(ctx context.Context, tx *sql.Tx, p outletDisplayLabelWireRow) (string, error) {
	if p.Status != "pending" {
		return p.Status, nil
	}
	if p.expiry <= a.tokenTime().Unix() {
		return "expired", nil
	}
	var revoked sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM mcp_proposal_sessions WHERE id=$1`, p.sessionID).Scan(&revoked); err != nil {
		return "", err
	}
	if revoked.Valid {
		return "revoked", nil
	}
	return "pending", nil
}

func (a *app) pruneOutletDisplayLabelProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_outlet_display_label_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_outlet_display_label_proposals p SET status=ended.state,label_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}

func (a *app) beginOutletDisplayLabelReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
	var zero [32]byte
	s, err := wikiProposalPath(r)
	if err != nil {
		return nil, s, zero, err
	}
	s.actor = u.id
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil, s, zero, err
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err == nil {
		err = a.lockOutletDisplayLabelScope(r.Context(), tx, reservationConfirmScopeFrom(s))
	}
	if err != nil {
		tx.Rollback()
		return nil, s, zero, err
	}
	login, err := proposalLoginHash(r)
	if err != nil {
		tx.Rollback()
		return nil, s, zero, err
	}
	return tx, s, login, nil
}
