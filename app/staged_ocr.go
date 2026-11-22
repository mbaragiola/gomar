package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

const ocrDisclosureVersion = "google-gemini-55-days-2026-10-09-v1"

var (
	disabledOCR       = problem{503, "OCR no está habilitado, aprobado o configurado. Cargá los datos a mano y después adjuntá el archivo."}
	refusedOCRConsent = problem{422, "Tenés que aceptar el aviso de privacidad para enviar este archivo a Gemini API."}
	staleOCRConsent   = problem{409, "El aviso de privacidad cambió. Revisalo y volvé a aceptar antes de usar OCR."}
	claimedOCR        = problem{409, "Esta carga ya se está procesando o ya tiene un resultado."}
)

type ocrConsent struct {
	Accepted *bool   `json:"acepta_envio_google"`
	Version  *string `json:"version_aviso"`
}

// stagedOCRDisabled is deliberately not an extractor. There is no provider
// or enabled production state. As in Django, the request must have the
// expected shape and scope, but the disabled gate precedes consent evaluation.
func (a *app) stagedOCRDisabled(w http.ResponseWriter, r *http.Request, u user) error {
	w.Header().Set("Cache-Control", "private, no-store")
	var consent ocrConsent
	if err := parseJSON(w, r, &consent); err != nil {
		return err
	}
	if consent.Accepted == nil || consent.Version == nil {
		return badInput
	}
	return a.scopedIntakeOCR(r, u, func(_ *sql.Tx, _ intakeRecord) error { return disabledOCR })
}

// scopedIntakeOCR owns the transaction through scope and expiry. The supplied
// operation may use scoped database and private-file reads; external work
// must run after it returns.
func (a *app) scopedIntakeOCR(r *http.Request, u user, action func(*sql.Tx, intakeRecord) error) error {
	tx, m, err := a.intakeScope(r, u, intakeDraft)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, err := scopedIntakeRecord(tx, m, intakeUUID(r.PathValue("operation")))
	if err != nil {
		return err
	}
	if op.live() && op.expires <= time.Now().UnixMicro() {
		if err := a.finishStagedIntake(r.Context(), tx, op, "expired"); err != nil {
			return err
		}
		return expiredIntake
	}
	return action(tx, op)
}

type stagedOCRClaim struct {
	intakeRecord
	eventID int64
}

// claimStagedOCR is a transaction prerequisite, not an HTTP handler. No
// shipped route calls it. A future extractor must run after this returns,
// outside this transaction, and recheck the live claim before completion.
func (a *app) claimStagedOCR(r *http.Request, u user, consent ocrConsent) (stagedOCRClaim, error) {
	var claimed stagedOCRClaim
	err := a.scopedIntakeOCR(r, u, func(tx *sql.Tx, op intakeRecord) error {
		if consent.Accepted == nil || consent.Version == nil {
			return badInput
		}
		if !*consent.Accepted {
			return refusedOCRConsent
		}
		if *consent.Version != ocrDisclosureVersion {
			return staleOCRConsent
		}
		now := time.Now().UnixMicro()
		result, err := tx.ExecContext(r.Context(), `UPDATE intake_operations SET status='processing'
			WHERE id=$1 AND status IN ('uploaded','failed') AND expires_at>$2`, op.id, now)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			if op.live() && op.expires <= now {
				if err := a.finishStagedIntake(r.Context(), tx, op, "expired"); err != nil {
					return err
				}
				return expiredIntake
			}
			return claimedOCR
		}
		var eventID int64
		err = tx.QueryRowContext(r.Context(), `INSERT INTO ocr_consent_events
			(actor_id,entity_id,outlet_id,operation_id,disclosure_version,action,occurred_at)
			SELECT user_id,entity_id,outlet_id,id,$1,'request_gemini_ocr',$2
			FROM intake_operations WHERE id=$3 RETURNING id`, ocrDisclosureVersion, now, op.id).Scan(&eventID)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("OCR consent event was not recorded")
		}
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		op.status = "processing"
		claimed = stagedOCRClaim{intakeRecord: op, eventID: eventID}
		return nil
	})
	return claimed, err
}
