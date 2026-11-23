package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type reservationPolicyNoticeInput struct {
	NoticeHours int `json:"notice_hours"`
}

type reservationPolicyNoticePreview struct {
	Scope        reservationConfirmScope `json:"-"`
	NoticeHours  int                     `json:"notice_hours"`
	Current      string                  `json:"current_policy"`
	Proposed     string                  `json:"policy"`
	VenueEnabled bool                    `json:"venue_enabled"`
}

var (
	reservationPolicyNoticeChanged        = problem{409, "Cambió la política o la configuración del local. Prepará otra propuesta."}
	reservationPolicyNoticeEnabled        = problem{409, "Solo se puede preparar la política de un local de reservas desactivado."}
	reservationPolicyNoticeNoChange       = problem{422, "El texto propuesto ya es la política actual."}
	reservationPolicyNoticeAcknowledgment = problem{422, "Confirmá que el texto es informativo y solo aplica a solicitudes nuevas después de habilitar las reservas por separado."}
)

func formatReservationPolicyNotice(hours int) (string, error) {
	if hours < 1 || hours > 72 {
		return "", badInput
	}
	unit := "horas"
	if hours == 1 {
		unit = "hora"
	}
	policy := fmt.Sprintf("Por favor, avisá al local con al menos %d %s de anticipación si no podés asistir.", hours, unit)
	// Keep the generated wording inside the native text-only bounds.
	if canonical, valid := reservationText(policy, 1, 500); !valid || policy != canonical {
		return "", badInput
	}
	return policy, nil
}

func decodeReservationPolicyNotice(raw []byte) (reservationPolicyNoticeInput, error) {
	var in reservationPolicyNoticeInput
	if len(raw) > 128 || decodeWikiProposalJSON(raw, &in, "notice_hours") != nil {
		return in, badInput
	}
	_, err := formatReservationPolicyNotice(in.NoticeHours)
	return in, err
}

func (p reservationPolicyNoticePreview) digest() [32]byte {
	data, _ := json.Marshal(struct {
		Action  string
		Scope   reservationConfirmScope
		Preview reservationPolicyNoticePreview
	}{reservationPolicyNoticeSessionAction + ".v1", p.Scope, p})
	return sha256.Sum256(data)
}

func (a *app) lockReservationPolicyNoticeScope(ctx context.Context, tx *sql.Tx, s reservationConfirmScope) error {
	if !a.reservationPolicyNoticeProposalsEnabled() {
		return wikiProposalUnavailable
	}
	return lockReservationConfirmScope(ctx, tx, s)
}

// Caller holds the same outlet lock as staff settings and public booking.
// Never reject a matching old policy while preparing a bearer proposal.
func readReservationPolicyNotice(ctx context.Context, tx *sql.Tx, s reservationConfirmScope, in reservationPolicyNoticeInput) (reservationPolicyNoticePreview, error) {
	text, err := formatReservationPolicyNotice(in.NoticeHours)
	if err != nil {
		return reservationPolicyNoticePreview{}, err
	}
	p := reservationPolicyNoticePreview{Scope: s, NoticeHours: in.NoticeHours, Proposed: text}
	err = tx.QueryRowContext(ctx, `SELECT cancellation_policy,enabled FROM reservation_venues WHERE outlet_id=$1`, s.Outlet).
		Scan(&p.Current, &p.VenueEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return p, reservationUnavailable
	}
	if err != nil {
		return p, err
	}
	if p.VenueEnabled {
		return p, reservationPolicyNoticeEnabled
	}
	return p, nil
}

func (a *app) applyReservationPolicyNotice(r *http.Request, tx *sql.Tx, u user, s reservationConfirmScope, p reservationPolicyNoticePreview, approved [32]byte) error {
	if u.id <= 0 || u.id != s.Actor {
		return unauthenticated
	}
	d := p.digest()
	if s != p.Scope || p.VenueEnabled || subtle.ConstantTimeCompare(d[:], approved[:]) != 1 {
		return reservationPolicyNoticeChanged
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	ctx := r.Context()
	if err := a.lockReservationPolicyNoticeScope(ctx, tx, s); err != nil {
		return err
	}
	fresh, err := readReservationPolicyNotice(ctx, tx, s, reservationPolicyNoticeInput{p.NoticeHours})
	if err != nil && err != reservationUnavailable && err != reservationPolicyNoticeEnabled && err != badInput {
		return err
	}
	if err != nil || fresh != p {
		return reservationPolicyNoticeChanged
	}
	if p.Current == p.Proposed {
		return reservationPolicyNoticeNoChange
	}
	result, err := tx.ExecContext(ctx, `UPDATE reservation_venues SET cancellation_policy=$1
		WHERE outlet_id=$2 AND NOT enabled AND cancellation_policy=$3`, p.Proposed, s.Outlet, p.Current)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return reservationPolicyNoticeChanged
	}
	return nil
}
