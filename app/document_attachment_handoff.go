package main

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
)

var missingDraftAttachmentHandoff = problem{404, "No se encontró un borrador disponible en ese local."}

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
	if !slices.Contains(capabilities[m.role], draftCapability) && !slices.Contains(capabilities[m.role], "download_attachments") {
		return forbiddenRole
	}
	if err := requireSelectedDocumentOutlet(tx, m, outlet); err != nil {
		if err == missingOutlet {
			return missingDraftAttachmentHandoff
		}
		return err
	}
	item, err := scanDocument(tx.QueryRowContext(ctx, selectDocuments+`
		AND d.id=$4 AND d.status='draft'
		AND NOT EXISTS (SELECT 1 FROM ap_payables p WHERE p.source_document_id=d.id)`, m.ID, m.id, outlet, id), m.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return missingDraftAttachmentHandoff
	}
	if err != nil {
		return err
	}
	view, err := completeDocumentView(tx, item)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, view)
	return nil
}
