package main

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
)

var missingDraftAttachmentHandoff = problem{404, "No se encontró un borrador disponible en ese local."}

type attachmentHandoffItem struct {
	ID   int64  `json:"id"`
	Name string `json:"nombre"`
	Size int64  `json:"tamano"`
}

type attachmentHandoffView struct {
	Entity      int64                   `json:"entity_id"`
	Outlet      int64                   `json:"outlet_id"`
	Document    int64                   `json:"document_id"`
	CanDownload bool                    `json:"can_download"`
	CanUpload   bool                    `json:"can_upload"`
	Attachments []attachmentHandoffItem `json:"adjuntos"`
}

func attachmentHandoffID(r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0 && id <= (1<<53)-1 && strconv.FormatInt(id, 10) == raw
}

// draftAttachmentHandoff is a browser detail read, not an attachment file
// route. It returns no link or capability and never opens private storage.
func (a *app) draftAttachmentHandoff(w http.ResponseWriter, r *http.Request, u user) error {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || len(r.Header.Values("Authorization")) != 0 {
		return badInput
	}
	entity, entityOK := attachmentHandoffID(r, "entity")
	outlet, outletOK := attachmentHandoffID(r, "outlet")
	id, docOK := attachmentHandoffID(r, "document")
	if !entityOK || !outletOK || !docOK {
		return missingDraftAttachmentHandoff
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, err := scopedMembership(tx, u.id, entity, "read_business_data")
	if err != nil {
		if err == missingEntity {
			return missingDraftAttachmentHandoff
		}
		return err
	}
	canUpload := slices.Contains(capabilities[m.role], draftCapability)
	canDownload := slices.Contains(capabilities[m.role], "download_attachments")
	if !canUpload && !canDownload {
		return forbiddenRole
	}
	if err := requireSelectedDocumentOutlet(tx, m, outlet); err != nil {
		if err == missingOutlet {
			return missingDraftAttachmentHandoff
		}
		return err
	}
	var documentID int64
	err = tx.QueryRowContext(ctx, `SELECT d.id FROM documents d
		WHERE d.id=$1 AND d.entity_id=$2 AND d.outlet_id=$3 AND d.status='draft' AND d.deleted_at IS NULL
		AND EXISTS (SELECT 1 FROM suppliers s WHERE s.id=d.supplier_id AND s.entity_id=d.entity_id)
		AND NOT EXISTS (SELECT 1 FROM ap_payables p WHERE p.source_document_id=d.id)`, id, m.ID, outlet).Scan(&documentID)
	if errors.Is(err, sql.ErrNoRows) {
		return missingDraftAttachmentHandoff
	}
	if err != nil {
		return err
	}
	view := attachmentHandoffView{Entity: m.ID, Outlet: outlet, Document: documentID,
		CanDownload: canDownload, CanUpload: canUpload, Attachments: []attachmentHandoffItem{}}
	rows, err := tx.QueryContext(ctx, `SELECT id,original_filename,size FROM document_attachments
		WHERE document_id=$1 ORDER BY created_at,id`, documentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item attachmentHandoffItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Size); err != nil {
			return err
		}
		view.Attachments = append(view.Attachments, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, view)
	return nil
}
