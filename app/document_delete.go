package main

import (
	"net/http"
	"time"
)

func (a *app) deleteDocument(w http.ResponseWriter, r *http.Request, u user) error {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	documentID, err := pathID(r, "document")
	if err != nil {
		return err
	}
	var removal draftMutationFields
	if err := parseJSON(w, r, &removal); err != nil {
		return err
	}
	if removal.Reason == nil || removal.Revision == nil {
		return badInput
	}
	reason := trimDraftText(*removal.Reason)
	if reason == "" {
		return problem{422, "El motivo de la baja es obligatorio."}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, current, err := scopedMutableDocument(tx, u.id, entityID, documentID,
		"La factura ya está registrada en cuentas a pagar y no se puede dar de baja.")
	if err != nil {
		return err
	}
	if err := a.lockDocumentWrite(r, tx, u, entityID); err != nil {
		return err
	}
	member, current, err = scopedMutableDocument(tx, u.id, entityID, documentID, "La factura ya está registrada en cuentas a pagar y no se puede dar de baja.")
	if err != nil {
		return err
	}
	revision := *removal.Revision
	if err := validateDraftRevision(current, revision); err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE documents SET deleted_at=$1,deleted_by_id=$2,updated_by_id=$3,updated_at=$4,
		revision=revision+1 WHERE id=$5 AND entity_id=$6 AND revision=$7 AND status=$8 AND deleted_at IS NULL`,
		at, member.userID, member.userID, at, documentID, member.ID, revision, current.Status)
	if err != nil {
		return err
	}
	if err := requireDraftChange(result); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO document_audit(document_id,actor_id,action,reason,occurred_at)
		VALUES ($1,$2,'delete',$3,$4)`, documentID, member.userID, reason, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, map[string]string{"detalle": "Comprobante dado de baja."})
	return nil
}
