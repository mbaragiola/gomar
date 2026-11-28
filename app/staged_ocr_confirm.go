package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"time"
)

const (
	intakeCreateReason = "Alta confirmada desde borrador de OCR"
	intakeAttachReason = "Archivo privado confirmado"
)

var (
	confirmNotReady         = problem{409, "La carga no tiene un borrador de OCR listo para confirmar."}
	confirmMovedOutlet      = problem{409, "Cambió el local de la carga. Descartá el borrador y volvé a empezar."}
	confirmSupplier         = problem{422, "Elegí y confirmá el proveedor que coincide exactamente con el CUIT emisor."}
	confirmInactiveSupplier = problem{409, "Ese CUIT pertenece a un proveedor inactivo en la entidad."}
	confirmDuplicate        = problem{409, "Ya existe el proveedor o el comprobante confirmado en esta entidad."}
)

type stagedTerminalCleanup func(context.Context, string) error

// confirmStagedPNG is mounted only in synthetic tests. It uses one immediate
// transaction for the READY claim, document, attachment and both audit events.
func (a *app) confirmStagedPNG(w http.ResponseWriter, r *http.Request, u user, cleanup stagedTerminalCleanup) error {
	w.Header().Set("Cache-Control", "private, no-store")
	body, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	input, err := decodeDraftFields(body, false)
	if err != nil {
		return err
	}
	var confirm struct {
		CreateSupplier bool `json:"crear_proveedor"`
	}
	if err := decodeJSON(body, &confirm); err != nil {
		return err
	}
	tx, m, err := a.intakeScope(r, u, intakeDraft)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, err := scopedIntakeRecord(tx, m, intakeUUID(r.PathValue("operation")))
	if err != nil {
		return err
	}
	if op.status == "expired" {
		return expiredIntake
	}
	if op.live() && op.expires <= time.Now().UnixMicro() {
		if err := a.finishStagedIntake(r.Context(), tx, op, "expired"); err != nil {
			return err
		}
		return expiredIntake
	}
	if op.status != "ready" {
		return confirmNotReady
	}
	if *input.Outlet != op.outletID {
		return confirmMovedOutlet
	}
	if confirm.CreateSupplier || op.mime != "image/png" {
		return problem{422, "Esta prueba solo admite PNG con un proveedor existente."}
	}
	if err := confirmExistingSupplier(tx, m.ID, input); err != nil {
		return err
	}
	data, err := a.readStagedBytes(op)
	if err != nil {
		return missingIntakeFile
	}
	id, err := insertDraft(tx, m, input, intakeCreateReason)
	if err == duplicateDocument {
		return confirmDuplicate
	}
	if err != nil {
		return err
	}
	name := newDocumentImageName()
	removeCopy, err := a.writeOwnedDocumentImage(name, data)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			removeCopy()
			if err := syncIntakeDirectory(a.privateRoot); err != nil {
				log.Print("could not sync private intake rollback")
			}
		}
	}()
	if err := syncIntakeDirectory(a.privateRoot); err != nil {
		return err
	}
	meta := receiptMetadata{Name: "comprobante.png", MIME: op.mime, Size: op.size}
	if _, err := insertDocumentImage(tx, id, u.id, name, meta, intakeAttachReason); err != nil {
		return err
	}
	now := time.Now().UnixMicro()
	if op.expires <= now {
		// This transaction must roll back document, attachment and copy. The
		// next authorized intake request can commit ordinary expiry on its own.
		return expiredIntake
	}
	if err := exactlyOneWrite(tx.ExecContext(r.Context(), `UPDATE intake_operations SET status='confirmed'
		WHERE id=$1 AND status='ready' AND expires_at>$2`, op.id, now)); err != nil {
		return err
	}
	item, err := readDocumentView(tx, m, id)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		if a.retainAfterDocumentCommitError(err) {
			committed = true // unknown outcome; do not unlink a possible reference
			log.Print("private image retained after an uncertain database commit")
		}
		return err
	}
	committed = true

	// A staged unlink is not part of document success. Keep its exact
	// tracked terminal reference if removal or its durable clear fails.
	if cleanup == nil {
		cleanup = a.cleanupTerminalIntake
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	if err := cleanup(ctx, op.id); err != nil {
		log.Print("could not finish confirmed staged image cleanup")
	}
	jsonResponse(w, http.StatusCreated, item)
	return nil
}

func confirmExistingSupplier(tx *sql.Tx, entityID int64, input draftInput) error {
	cuit, err := normalizeCUIT(*input.Issuer)
	if err != nil {
		return err
	}
	var id int64
	var active bool
	err = tx.QueryRow(`SELECT id,active FROM suppliers WHERE entity_id=$1 AND cuit=$2`, entityID, cuit).Scan(&id, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return confirmSupplier
	}
	if err != nil {
		return err
	}
	if !active {
		return confirmInactiveSupplier
	}
	if input.Supplier == nil || *input.Supplier != id {
		return confirmSupplier
	}
	return nil
}
