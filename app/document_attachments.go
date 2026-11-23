package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type documentAttachment struct {
	ID int64 `json:"id"`
	receiptMetadata
	UploadedAt string `json:"subido"`
}

var missingDocumentAttachment = problem{404, "No se encontró el archivo adjunto."}

func readDocumentAttachments(tx *sql.Tx, documentID int64) ([]documentAttachment, error) {
	rows, err := tx.Query(`SELECT id,original_filename,mime_type,size,created_at
		FROM document_attachments WHERE document_id=$1 ORDER BY created_at,id`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attachments := []documentAttachment{}
	for rows.Next() {
		var item documentAttachment
		if err := rows.Scan(&item.ID, &item.Name, &item.MIME, &item.Size, &item.UploadedAt); err != nil {
			return nil, err
		}
		attachments = append(attachments, item)
	}
	return attachments, rows.Err()
}

func visibleAttachmentDocument(tx *sql.Tx, userID, entityID, documentID int64) (entityMembership, documentListItem, error) {
	member, err := scopedMembership(tx, userID, entityID, "read_business_data")
	if err != nil {
		return member, documentListItem{}, err
	}
	doc, err := scanDocument(tx.QueryRow(selectDocuments+" AND d.id=$4", member.ID, member.id, nil, documentID), member.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = problem{404, "No se encontró el comprobante."}
	}
	return member, doc, err
}

func attachmentUploadRole(member entityMembership, doc documentListItem) error {
	required := draftCapability
	if doc.Status == "reviewed" {
		required = "manage_reviewed_documents"
	}
	if !slices.Contains(capabilities[member.role], required) {
		return forbiddenRole
	}
	return nil
}

func (a *app) expectedAttachmentDocument(r *http.Request, u user, entityID, documentID int64) (documentListItem, error) {
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return documentListItem{}, err
	}
	defer tx.Rollback()
	member, target, err := visibleAttachmentDocument(tx, u.id, entityID, documentID)
	if err != nil {
		return target, err
	}
	return target, attachmentUploadRole(member, target)
}

func (a *app) uploadDocumentImage(w http.ResponseWriter, r *http.Request, u user) error {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	documentID, err := pathID(r, "document")
	if err != nil {
		return err
	}
	before, err := a.expectedAttachmentDocument(r, u, entityID, documentID)
	if err != nil {
		return err
	}
	meta, bytes, err := browserPrivateImage(w, r, "Los adjuntos PDF todavía no están disponibles en esta versión.")
	if err != nil {
		return err
	}
	tx, err := a.beginEmployeeCheckedWrite(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, current, err := visibleAttachmentDocument(tx, u.id, entityID, documentID)
	if err != nil {
		return err
	}
	if err := attachmentUploadRole(member, current); err != nil {
		return err
	}
	if err := a.lockDocumentWrite(r, tx, u, entityID); err != nil {
		return err
	}
	member, current, err = visibleAttachmentDocument(tx, u.id, entityID, documentID)
	if err != nil {
		return err
	}
	if current.Revision != before.Revision || current.Status != before.Status || current.Outlet.ID != before.Outlet.ID {
		return draftRevisionConflict
	}
	if err := attachmentUploadRole(member, current); err != nil {
		return err
	}
	name := newDocumentImageName()
	removeCopy, err := a.writeOwnedDocumentImage(name, bytes)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			removeCopy()
		}
	}()
	if err := syncIntakeDirectory(a.privateRoot); err != nil {
		return err
	}
	item, err := insertDocumentImage(tx, documentID, u.id, name, meta, "Adjunto: "+meta.Name)
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
	jsonResponse(w, http.StatusCreated, item)
	return nil
}

func newDocumentImageName() string {
	var random [16]byte
	rand.Read(random[:])
	return "document-" + hex.EncodeToString(random[:])
}

// writeOwnedDocumentImage owns no path until O_EXCL succeeds. Its cleanup
// will not remove a preexisting name or a different inode.
func (a *app) writeOwnedDocumentImage(name string, data []byte) (func(), error) {
	if a.privateRoot == nil {
		return nil, errors.New("private attachment storage not configured")
	}
	file, err := a.privateRoot.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	owned, statErr := file.Stat()
	cleanup := func() {
		current, err := a.privateRoot.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil || owned == nil || !os.SameFile(owned, current) {
			log.Print("could not verify uncommitted private document image")
			return
		}
		if err := a.privateRoot.Remove(name); err != nil {
			log.Print("could not remove uncommitted private document image")
		}
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(statErr, writeErr, file.Sync(), file.Close()); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}

func exactlyOneWrite(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("expected exactly one database row to change")
	}
	return nil
}

func insertDocumentEvent(tx *sql.Tx, documentID, actorID int64, action, reason, at string) error {
	return exactlyOneWrite(tx.Exec(`INSERT INTO document_audit(document_id,actor_id,action,reason,occurred_at)
		VALUES ($1,$2,$3,$4,$5)`, documentID, actorID, action, reason, at))
}

func insertDocumentImage(tx *sql.Tx, documentID, actorID int64, name string, meta receiptMetadata, reason string) (documentAttachment, error) {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	var attachmentID int64
	err := tx.QueryRow(`INSERT INTO document_attachments(document_id,storage_name,original_filename,mime_type,size,created_by_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, documentID, name, meta.Name, meta.MIME, meta.Size, actorID, at).Scan(&attachmentID)
	if err != nil {
		return documentAttachment{}, err
	}
	if err := insertDocumentEvent(tx, documentID, actorID, "attach", reason, at); err != nil {
		return documentAttachment{}, err
	}
	return documentAttachment{attachmentID, meta, at}, nil
}

func (a *app) downloadDocumentImage(w http.ResponseWriter, r *http.Request, u user) error {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	documentID, err := pathID(r, "document")
	if err != nil {
		return err
	}
	attachmentID, err := pathID(r, "attachment")
	if err != nil {
		return err
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, _, err := visibleAttachmentDocument(tx, u.id, entityID, documentID)
	if err != nil {
		return err
	}
	var meta receiptMetadata
	var name string
	err = tx.QueryRow(`SELECT storage_name,original_filename,mime_type,size FROM document_attachments
		WHERE id=$1 AND document_id=$2`, attachmentID, documentID).Scan(&name, &meta.Name, &meta.MIME, &meta.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return missingDocumentAttachment
	}
	if err != nil {
		return err
	}
	if !slices.Contains(capabilities[member.role], "download_attachments") {
		return problem{403, "Tu rol no permite descargar archivos adjuntos."}
	}
	if a.privateRoot == nil {
		return missingDocumentAttachment
	}
	file, err := a.privateRoot.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return missingDocumentAttachment
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != meta.Size {
		return missingDocumentAttachment
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": meta.Name})
	if strings.IndexFunc(meta.Name, func(r rune) bool { return r > 127 }) < 0 {
		disposition = fmt.Sprintf("attachment; filename=%q", meta.Name)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", meta.MIME)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	if _, err := io.CopyN(w, file, meta.Size); err != nil {
		log.Print("incomplete private document image download")
	}
	return nil
}
