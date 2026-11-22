package main

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"time"
)

func (a *app) reviewDraft(w http.ResponseWriter, r *http.Request, u user) error {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	documentID, err := pathID(r, "document")
	if err != nil {
		return err
	}
	var input draftMutationFields
	if err := parseJSON(w, r, &input); err != nil {
		return err
	}
	if input.Reason == nil || input.Revision == nil {
		return badInput
	}
	reason := trimDraftText(*input.Reason)
	if reason == "" {
		return problem{422, "El motivo de la revisión es obligatorio."}
	}

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entityID, "read_business_data")
	if err != nil {
		return err
	}
	current, err := scanDocument(tx.QueryRow(selectDocuments+" AND d.id=$4", member.ID, member.id, nil, documentID), member.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return problem{404, "No se encontró el comprobante."}
	}
	if err != nil {
		return err
	}
	if !slices.Contains(capabilities[member.role], "review_documents") {
		return forbiddenRole
	}
	if err := a.lockDocumentWrite(r, tx, u, entityID); err != nil {
		return err
	}
	member, current, err = visibleAttachmentDocument(tx, u.id, entityID, documentID)
	if err != nil {
		return err
	}
	if !slices.Contains(capabilities[member.role], "review_documents") {
		return forbiddenRole
	}
	if current.Status != "draft" {
		return draftRevisionConflict
	}
	revision := *input.Revision
	if err := validateDraftRevision(current, revision); err != nil {
		return err
	}
	if err := applyNativeDraftReview(tx, member, documentID, revision, reason); err != nil {
		return err
	}
	document, err := readDocumentView(tx, member, documentID)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, document)
	return nil
}

func applyNativeDraftReview(tx *sql.Tx, member entityMembership, documentID, revision int64, reason string) error {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE documents SET status='reviewed',revision=revision+1,updated_at=$1,updated_by_id=$2
		WHERE id=$3 AND entity_id=$4 AND revision=$5 AND status='draft' AND deleted_at IS NULL`,
		at, member.userID, documentID, member.ID, revision)
	if err != nil {
		return err
	}
	if err := requireDraftChange(result); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO document_audit(document_id,actor_id,action,reason,occurred_at)
		VALUES ($1,$2,'review',$3,$4)`, documentID, member.userID, reason, at); err != nil {
		return err
	}
	return nil
}
