package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"time"
)

var (
	stagedOCRFailed  = problem{502, "No se pudo procesar el archivo con OCR. Podés completar los datos manualmente."}
	stagedOCRInvalid = problem{502, "El proveedor devolvió una respuesta que no se pudo validar. Podés completar los datos manualmente."}
)

// stagedOCRExtractor is a synthetic test seam. There is no implementation or
// route to it in the ordinary executable. Do not add runtime activation.
type stagedOCRExtractor func(context.Context, []byte, string) ([]byte, error)

func (a *app) stagedOCRDraft(w http.ResponseWriter, r *http.Request, u user, extract stagedOCRExtractor) error {
	w.Header().Set("Cache-Control", "private, no-store")
	if extract == nil {
		return disabledOCR
	}
	var consent ocrConsent
	if err := parseJSON(w, r, &consent); err != nil {
		return err
	}
	claim, err := a.claimStagedOCR(r, u, consent)
	if err != nil {
		return err
	}
	ready := false
	defer func() {
		if !ready {
			a.failStagedOCRClaim(r.Context(), claim)
		}
	}()
	source, err := a.ocrClaimSource(r, u, claim)
	if err != nil {
		return err
	}
	// ocrClaimSource has closed the file and released all database locks.
	if err := r.Context().Err(); err != nil {
		return err
	}
	annotation, err := extract(r.Context(), source, claim.mime)
	if err != nil {
		return stagedOCRFailed
	}
	draft, err := a.finishStagedOCRDraft(r, u, claim, annotation)
	if err != nil {
		return err
	}
	ready = true
	jsonResponse(w, 200, map[string]any{"estado": "borrador_sin_guardar", "borrador": draft})
	return nil
}

// The event id distinguishes this claim even if a later retry moves the same
// row back into processing with identical staged metadata.
const latestOCREvent = `(SELECT MAX(id) FROM ocr_consent_events WHERE operation_id=intake_operations.id)`

func (a *app) withCurrentOCRClaim(r *http.Request, u user, claim stagedOCRClaim, action func(*sql.Tx, intakeRecord) error) error {
	return a.scopedIntakeOCR(r, u, func(tx *sql.Tx, op intakeRecord) error {
		if op != claim.intakeRecord {
			return claimedOCR
		}
		var eventID sql.NullInt64
		if err := tx.QueryRowContext(r.Context(), `SELECT `+latestOCREvent+` FROM intake_operations WHERE id=$1`, op.id).Scan(&eventID); err != nil {
			return err
		}
		if !eventID.Valid || eventID.Int64 != claim.eventID {
			return claimedOCR
		}
		return action(tx, op)
	})
}

func (a *app) ocrClaimSource(r *http.Request, u user, claim stagedOCRClaim) ([]byte, error) {
	check := func() error {
		return a.withCurrentOCRClaim(r, u, claim, func(*sql.Tx, intakeRecord) error { return nil })
	}
	if err := check(); err != nil {
		return nil, err
	}
	// The bounded file read is outside any write or scoping transaction. It
	// yields a closed, digest-checked snapshot, not an open file descriptor.
	source, readErr := a.readStagedBytes(claim.intakeRecord)
	// A grant or claim can change during the read. Re-check it before handing
	// the snapshot to the synthetic callback, without holding any lock there.
	if err := check(); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, missingIntakeFile
	}
	return source, nil
}

func (a *app) finishStagedOCRDraft(r *http.Request, u user, claim stagedOCRClaim, annotation []byte) (map[string]any, error) {
	var draft map[string]any
	err := a.withCurrentOCRClaim(r, u, claim, func(tx *sql.Tx, op intakeRecord) error {
		if _, err := a.readStagedBytes(op); err != nil {
			return missingIntakeFile
		}
		var entityCUIT string
		if err := tx.QueryRowContext(r.Context(), `SELECT cuit FROM entities WHERE id=$1`, op.entityID).Scan(&entityCUIT); err != nil {
			return err
		}
		var lookupErr error
		var err error
		draft, err = ocrSuggestions(annotation, entityCUIT, func(cuit string) *ocrSupplier {
			var match ocrSupplier
			lookupErr = tx.QueryRowContext(r.Context(), `SELECT id,name,cuit,active FROM suppliers
				WHERE entity_id=$1 AND cuit=$2`, op.entityID, cuit).Scan(&match.ID, &match.Name, &match.CUIT, &match.Active)
			if errors.Is(lookupErr, sql.ErrNoRows) {
				lookupErr = nil
				return nil
			}
			if lookupErr != nil {
				return nil
			}
			return &match
		})
		if err != nil {
			return stagedOCRInvalid
		}
		if lookupErr != nil {
			return lookupErr
		}
		now := time.Now().UnixMicro()
		if op.expires <= now {
			if err := a.finishStagedIntake(r.Context(), tx, op, "expired"); err != nil {
				return err
			}
			return expiredIntake
		}
		res, err := tx.ExecContext(r.Context(), `UPDATE intake_operations SET status='ready'
			WHERE id=$1 AND status='processing' AND expires_at>$2 AND `+latestOCREvent+`=$3`, op.id, now, claim.eventID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return claimedOCR
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	return draft, nil
}

func (a *app) failStagedOCRClaim(request context.Context, claim stagedOCRClaim) {
	// A canceled client can still release only its own claim for a fresh,
	// explicit retry. Never let the delayed cleanup overwrite a newer claim.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(request), 2*time.Second)
	defer cancel()
	if _, err := a.db.ExecContext(ctx, `UPDATE intake_operations SET status='failed'
		WHERE id=$1 AND status='processing' AND `+latestOCREvent+`=$2`, claim.id, claim.eventID); err != nil {
		log.Print("could not release staged OCR claim")
	}
}
