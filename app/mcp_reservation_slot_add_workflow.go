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

const reservationSlotAddSessionAction = "reservation_slot_add"

func (a *app) reservationSlotAddProposalsEnabled() bool {
	return a.enableMCPReservationSlotAddProposals
}

const reservationSlotAddWireBrowser = "/api/v1/entities/{entity}/outlets/{outlet}/reservation-slot-add-proposals"
const reservationSlotAddWireAPI = "/api/v1/mcp/entities/{entity}/outlets/{outlet}/reservation-slot-add-proposals"

type reservationSlotAddWireReceipt struct {
	ID      int64   `json:"id"`
	Status  string  `json:"status"`
	Expires string  `json:"expires_at"`
	StartAt *string `json:"start_at,omitempty"`
}
type reservationSlotAddWireDetails struct {
	reservationSlotAddWireReceipt
	SlotAdd *reservationSlotAddPreview `json:"slot_add,omitempty"`
	Digest  string                     `json:"digest,omitempty"`
}
type reservationSlotAddWireRow struct {
	reservationSlotAddWireReceipt
	sessionID, expiry                int64
	inputDigest, slotDigest, preview []byte
}

const reservationSlotAddWireColumns = `p.id,p.session_id,p.status,p.expires_at,p.approved_start_at,p.input_digest,p.slot_digest,p.slot_preview`

func scanReservationSlotAddProposal(row interface{ Scan(...any) error }) (reservationSlotAddWireRow, error) {
	var p reservationSlotAddWireRow
	err := row.Scan(&p.ID, &p.sessionID, &p.Status, &p.expiry, &p.StartAt, &p.inputDigest, &p.slotDigest, &p.preview)
	if errors.Is(err, sql.ErrNoRows) {
		return p, wikiProposalMissing
	}
	p.Expires = time.Unix(p.expiry, 0).UTC().Format(time.RFC3339)
	return p, err
}

func (a *app) registerMCPReservationSlotAddProposals(add func(string, string, http.HandlerFunc)) {
	if !a.reservationSlotAddProposalsEnabled() {
		return
	}
	add(reservationSlotAddWireBrowser, http.MethodGet, a.protectedWikiProposal(a.listReservationSlotAddProposals))
	add(reservationSlotAddWireBrowser+"/{proposal}", http.MethodGet, a.protectedWikiProposal(a.viewReservationSlotAddProposal))
	add(reservationSlotAddWireBrowser+"/{proposal}/decision", http.MethodPost, a.protectedWikiProposal(a.decideReservationSlotAddProposal))
	add(reservationSlotAddWireAPI, http.MethodPost, a.submitReservationSlotAddProposal)
}

func reservationSlotAddWireInputDigest(s reservationConfirmScope, input reservationSlotAddInput) [32]byte {
	data, _ := json.Marshal(struct {
		Kind  string
		Scope reservationConfirmScope
		Input reservationSlotAddInput
	}{reservationSlotAddSessionAction, s, input})
	return sha256.Sum256(data)
}

func (a *app) submitReservationSlotAddProposal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := a.acceptReservationSlotAddProposal(w, r); err != nil {
		if err == unauthenticated {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		failure(w, err)
	}
}

func (a *app) acceptReservationSlotAddProposal(w http.ResponseWriter, r *http.Request) error {
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
		RequestID string          `json:"request_id"`
		SlotAdd   json.RawMessage `json:"slot_add"`
	}
	if err = decodeWikiProposalJSON(raw, &body, "request_id", "slot_add"); err != nil {
		return err
	}
	if !wikiRequestID.MatchString(body.RequestID) {
		return badInput
	}
	in, err := decodeReservationSlotAdd(body.SlotAdd)
	if err != nil {
		return err
	}

	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, expiry, err := a.loadNamedProposalSession(ctx, tx, hash, &scope, reservationSlotAddSessionAction)
	if err != nil {
		return err
	}
	s := reservationConfirmScopeFrom(scope)
	if err = a.lockReservationSlotAddScope(ctx, tx, s); err != nil {
		return err
	}
	inputDigest := reservationSlotAddWireInputDigest(s, in)
	p, err := scanReservationSlotAddProposal(tx.QueryRowContext(ctx, `SELECT `+reservationSlotAddWireColumns+` FROM mcp_reservation_slot_add_proposals p
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
			FROM mcp_reservation_slot_add_proposals WHERE actor_id=$1 AND expires_at>$3`,
			scope.actor, now.Unix(), now.Add(-wikiProposalRetention).Unix()).Scan(&pending, &recent)
		if err != nil {
			return err
		}
		if pending >= 12 || recent >= 60 {
			return wikiProposalLimit
		}
		prepared, err := readReservationSlotAdd(ctx, tx, s, in, a.tokenTime())
		if err != nil {
			return err
		}
		slotDigest := prepared.digest()
		preview, err := json.Marshal(prepared)
		if err != nil {
			return err
		}
		now = a.tokenTime()
		if now.Unix() >= expiry {
			return unauthenticated
		}
		start, err := time.Parse(time.RFC3339, prepared.StartAt)
		if err != nil || start.Unix() <= now.Unix() {
			return reservationSlotAddElapsed
		}
		if start.Unix() < expiry {
			expiry = start.Unix()
		}
		p, err = scanReservationSlotAddProposal(tx.QueryRowContext(ctx, `INSERT INTO mcp_reservation_slot_add_proposals AS p
			(session_id,actor_id,entity_id,outlet_id,request_id,input_digest,slot_digest,
			slot_preview,status,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10) RETURNING `+reservationSlotAddWireColumns,
			session, scope.actor, scope.entity, scope.outlet, body.RequestID, inputDigest[:], slotDigest[:], preview, now.Unix(), expiry))
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
	p.StartAt = nil
	jsonResponse(w, status, p.reservationSlotAddWireReceipt)
	return nil
}

func (a *app) listReservationSlotAddProposals(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginReservationSlotAddReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := a.tokenTime()
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,
		CASE WHEN p.status='pending' AND p.expires_at<=$4 THEN 'expired'
		     WHEN p.status='pending' AND t.revoked_at IS NOT NULL THEN 'revoked'
		     ELSE p.status END,p.expires_at,p.approved_start_at
		FROM mcp_reservation_slot_add_proposals p JOIN mcp_proposal_sessions t ON t.id=p.session_id
		WHERE p.actor_id=$1 AND p.entity_id=$2 AND p.outlet_id=$3 AND t.login_hash=$5 AND p.expires_at>$6
		ORDER BY (p.status='pending' AND p.expires_at>$4 AND t.revoked_at IS NULL) DESC,p.id DESC LIMIT 30`,
		s.actor, s.entity, s.outlet, now.Unix(), login[:], now.Add(-wikiProposalRetention).Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []reservationSlotAddWireReceipt{}
	for rows.Next() {
		var item reservationSlotAddWireReceipt
		var expiry int64
		if err = rows.Scan(&item.ID, &item.Status, &expiry, &item.StartAt); err != nil {
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

func lookupReservationSlotAddProposal(r *http.Request, tx *sql.Tx, s wikiProposalScope, login [32]byte) (reservationSlotAddWireRow, error) {
	id, err := pathID(r, "proposal")
	if err != nil || id <= 0 {
		return reservationSlotAddWireRow{}, wikiProposalMissing
	}
	return scanReservationSlotAddProposal(tx.QueryRowContext(r.Context(), `SELECT `+reservationSlotAddWireColumns+` FROM mcp_reservation_slot_add_proposals p
		JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.id=$1 AND p.actor_id=$2 AND p.entity_id=$3 AND p.outlet_id=$4 AND k.login_hash=$5
		FOR UPDATE OF p`, id, s.actor, s.entity, s.outlet, login[:]))
}

func (p reservationSlotAddWireRow) validatedReservationSlotAdd(s wikiProposalScope) (reservationSlotAddPreview, error) {
	var preview reservationSlotAddPreview
	if len(p.slotDigest) != 32 || json.Unmarshal(p.preview, &preview) != nil {
		return preview, reservationSlotAddChanged
	}
	preview.Scope = reservationConfirmScopeFrom(s)
	got := preview.digest()
	if subtle.ConstantTimeCompare(got[:], p.slotDigest) != 1 {
		return preview, reservationSlotAddChanged
	}
	return preview, nil
}

func (a *app) viewReservationSlotAddProposal(w http.ResponseWriter, r *http.Request, u user) error {
	tx, s, login, err := a.beginReservationSlotAddReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupReservationSlotAddProposal(r, tx, s, login)
	if err != nil {
		return err
	}
	if p.expiry <= a.tokenTime().Add(-wikiProposalRetention).Unix() {
		return wikiProposalMissing
	}
	p.Status, err = a.reservationSlotAddProposalState(r.Context(), tx, p)
	if err != nil {
		return err
	}
	result := reservationSlotAddWireDetails{reservationSlotAddWireReceipt: p.reservationSlotAddWireReceipt}
	if p.Status == "pending" {
		draft, err := p.validatedReservationSlotAdd(s)
		if err != nil {
			return err
		}
		result.SlotAdd = &draft
		result.Digest = hex.EncodeToString(p.slotDigest)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, result)
	return nil
}

func setReservationSlotAddProposalTerminal(ctx context.Context, tx *sql.Tx, id int64, status string, now int64, start *string) error {
	return exactlyOneWrite(tx.ExecContext(ctx, `UPDATE mcp_reservation_slot_add_proposals SET status=$1,slot_preview=NULL,
		decided_at=$2,approved_start_at=$3 WHERE id=$4 AND status='pending'`, status, now, start, id))
}

func (a *app) decideReservationSlotAddProposal(w http.ResponseWriter, r *http.Request, u user) error {
	raw, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	var input struct {
		Decision            string `json:"decision"`
		Digest              string `json:"digest"`
		Confirm             bool   `json:"confirm"`
		AcknowledgeNoRemove *bool  `json:"acknowledge_no_remove"`
	}
	if err = decodeWikiProposalJSON(raw, &input, "decision", "digest", "confirm"); err != nil {
		if decodeWikiProposalJSON(raw, &input, "decision", "digest", "confirm", "acknowledge_no_remove") != nil || input.AcknowledgeNoRemove == nil {
			return badInput
		}
	}
	if !input.Confirm || (input.Decision != "approve" && input.Decision != "reject") || len(input.Digest) != 64 ||
		input.Digest != strings.ToLower(input.Digest) {
		return badInput
	}
	var confirmed [32]byte
	if _, err = hex.Decode(confirmed[:], []byte(input.Digest)); err != nil {
		return badInput
	}
	tx, s, login, err := a.beginReservationSlotAddReview(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := lookupReservationSlotAddProposal(r, tx, s, login)
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
	state, err := a.reservationSlotAddProposalState(ctx, tx, p)
	if err != nil {
		return err
	}
	if state != "pending" {
		if err = setReservationSlotAddProposalTerminal(ctx, tx, p.ID, state, a.tokenTime().Unix(), nil); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return wikiProposalUnavailable
	}
	if subtle.ConstantTimeCompare(confirmed[:], p.slotDigest) != 1 {
		return wikiProposalConflict
	}
	draft, err := p.validatedReservationSlotAdd(s)
	if err != nil {
		return err
	}
	p.Status = "rejected"
	if input.Decision == "approve" {
		if input.AcknowledgeNoRemove == nil || !*input.AcknowledgeNoRemove {
			return reservationSlotAddAcknowledgment
		}
		if err := a.applyReservationSlotAdd(r, tx, u, reservationConfirmScopeFrom(s), draft, confirmed); err != nil {
			return err
		}
		p.StartAt = &draft.StartAt
		p.Status = "approved"
	}
	if _, err = lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	now := a.tokenTime().Unix()
	if p.expiry <= now {
		return wikiProposalUnavailable
	}
	if err = setReservationSlotAddProposalTerminal(ctx, tx, p.ID, p.Status, now, p.StartAt); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, p.reservationSlotAddWireReceipt)
	return nil
}

func (a *app) reservationSlotAddProposalState(ctx context.Context, tx *sql.Tx, p reservationSlotAddWireRow) (string, error) {
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

func (a *app) pruneReservationSlotAddProposals(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `WITH ended AS (
		SELECT p.id,CASE WHEN p.expires_at<=$1 THEN 'expired' ELSE 'revoked' END AS state
		FROM mcp_reservation_slot_add_proposals p JOIN mcp_proposal_sessions k ON k.id=p.session_id
		WHERE p.status='pending' AND (p.expires_at<=$1 OR k.revoked_at IS NOT NULL OR NOT EXISTS(
			SELECT 1 FROM sessions l WHERE l.token_hash=k.login_hash AND l.user_id=k.actor_id
				AND l.password_digest=k.password_digest AND l.expires>$2))
		ORDER BY p.expires_at,p.id LIMIT 100 FOR UPDATE OF p SKIP LOCKED
	) UPDATE mcp_reservation_slot_add_proposals p SET status=ended.state,slot_preview=NULL,decided_at=$1
		FROM ended WHERE p.id=ended.id`, now.Unix(), time.Now().Unix())
	return err
}

func (a *app) beginReservationSlotAddReview(r *http.Request, u user) (*sql.Tx, wikiProposalScope, [32]byte, error) {
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
		err = a.lockReservationSlotAddScope(r.Context(), tx, reservationConfirmScopeFrom(s))
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
