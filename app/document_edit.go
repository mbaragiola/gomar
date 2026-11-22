package main

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"time"
)

type draftMutationFields struct {
	Reason   *string `json:"motivo"`
	Revision *int64  `json:"expected_revision"`
}

var draftRevisionConflict = problem{409, "El documento fue modificado por otro usuario. Recargá la página."}

func (a *app) editDocument(w http.ResponseWriter, r *http.Request, u user) error {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	documentID, err := pathID(r, "document")
	if err != nil {
		return err
	}
	body, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	data, err := decodeDraft(body)
	if err != nil {
		return err
	}
	var edit draftMutationFields
	if err := decodeJSON(body, &edit); err != nil {
		return err
	}
	if edit.Reason == nil || edit.Revision == nil {
		return badInput
	}
	reason := trimDraftText(*edit.Reason)
	if reason == "" {
		return problem{422, "El motivo de la modificación es obligatorio."}
	}

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, current, err := scopedMutableDocument(tx, u.id, entityID, documentID,
		"La factura ya está registrada en cuentas a pagar y no se puede modificar.")
	if err != nil {
		return err
	}
	if err := a.lockDocumentWrite(r, tx, u, entityID); err != nil {
		return err
	}
	member, current, err = scopedMutableDocument(tx, u.id, entityID, documentID, "La factura ya está registrada en cuentas a pagar y no se puede modificar.")
	if err != nil {
		return err
	}
	revision := *edit.Revision
	if err := validateDraftRevision(current, revision); err != nil {
		return err
	}
	values, err := validateDraft(tx, member, data)
	if err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	changed, err := tx.Exec(`UPDATE documents SET outlet_id=$1,supplier_id=$2,issuer_cuit=$3,document_type=$4,
		letter=$5,point_of_sale=$6,number=$7,issue_date=$8,due_date=$9,currency=$10,subtotal_minor=$11,
		tax_minor=$12,total_minor=$13,description=$14,status='draft',revision=revision+1,updated_at=$15,updated_by_id=$16
		WHERE id=$17 AND entity_id=$18 AND revision=$19 AND status=$20 AND deleted_at IS NULL`,
		*data.Outlet, *data.Supplier, values.CUIT, *data.Kind, values.Letter, *data.Point, *data.Number,
		*data.Issue, data.Due, *data.Currency, values.Net, values.Tax, values.Total,
		trimDraftText(data.Description), at, member.userID, documentID, member.ID, revision, current.Status)
	if isDocumentIdentityConflict(err) {
		return duplicateDocument
	}
	if err != nil {
		return err
	}
	if err := recordNativeDraftUpdate(tx, changed, documentID, member.userID, reason, at); err != nil {
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

func recordNativeDraftUpdate(tx *sql.Tx, changed sql.Result, documentID, actorID int64, reason, at string) error {
	if err := requireDraftChange(changed); err != nil {
		return err
	}
	return insertDocumentEvent(tx, documentID, actorID, "update", reason, at)
}

func scopedMutableDocument(tx *sql.Tx, userID, entityID, documentID int64, postedMessage string) (entityMembership, documentListItem, error) {
	member, err := scopedMembership(tx, userID, entityID, "read_business_data")
	if err != nil {
		return member, documentListItem{}, err
	}
	current, err := scanDocument(tx.QueryRow(selectDocuments+" AND d.id=$4", member.ID, member.id, nil, documentID), member.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return member, current, problem{404, "No se encontró el comprobante."}
	}
	if err != nil {
		return member, current, err
	}
	if current.PayableID != nil {
		return member, current, problem{409, postedMessage}
	}
	need := draftCapability
	if current.Status != "draft" {
		need = "manage_reviewed_documents"
	}
	if !slices.Contains(capabilities[member.role], need) {
		return member, current, forbiddenRole
	}
	return member, current, nil
}

func validateDraftRevision(current documentListItem, revision int64) error {
	if revision < 1 {
		return problem{422, "La revisión esperada no es válida."}
	}
	if current.Revision != revision {
		return draftRevisionConflict
	}
	return nil
}

func requireDraftChange(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return draftRevisionConflict
	}
	return nil
}
