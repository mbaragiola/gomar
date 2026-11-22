package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type documentEditValues struct {
	IssueDate string  `json:"issue_date"`
	DueDate   *string `json:"due_date"`
	NetMinor  *int64  `json:"net_minor"`
	TaxMinor  *int64  `json:"tax_minor"`
	Total     int64   `json:"total_minor"`
}

type documentEditInput struct {
	ID       int64              `json:"document_id"`
	Revision int64              `json:"expected_revision"`
	Reason   string             `json:"reason"`
	After    documentEditValues `json:"after"`
}

type documentEditPreview struct {
	Scope    documentReviewScope
	Input    documentEditInput
	Before   documentEditValues
	Currency string
}

func decodeDocumentEdit(raw []byte) (documentEditInput, error) {
	var wire struct {
		ID       int64           `json:"document_id"`
		Revision *int64          `json:"expected_revision"`
		Reason   string          `json:"reason"`
		After    json.RawMessage `json:"after"`
	}
	var after struct {
		documentEditValues
		Total *int64 `json:"total_minor"`
	}
	if len(raw) > 3072 {
		return documentEditInput{}, badInput
	}
	if err := decodeWikiProposalJSON(raw, &wire, "document_id", "expected_revision", "reason", "after"); err != nil {
		return documentEditInput{}, err
	}
	if err := decodeWikiProposalJSON(wire.After, &after, "issue_date", "due_date", "net_minor", "tax_minor", "total_minor"); err != nil {
		return documentEditInput{}, err
	}
	if wire.Revision == nil || after.Total == nil {
		return documentEditInput{}, badInput
	}
	after.documentEditValues.Total = *after.Total
	return normalizeDocumentEdit(documentEditInput{wire.ID, *wire.Revision, wire.Reason, after.documentEditValues})
}

func normalizeDocumentEdit(in documentEditInput) (documentEditInput, error) {
	in.Reason = trimDraftText(in.Reason)
	v := &in.After
	if in.ID <= 0 || in.Revision < 0 || in.Revision == 1<<63-1 ||
		in.Reason == "" || len(in.Reason) > 1024 || !utf8.ValidString(in.Reason) || strings.ContainsRune(in.Reason, 0) ||
		!documentProposalDate(v.IssueDate) ||
		v.DueDate != nil && (!documentProposalDate(*v.DueDate) || *v.DueDate < v.IssueDate) ||
		v.Total < 0 || v.Total > maxDocumentDraftMinor || (v.NetMinor == nil) != (v.TaxMinor == nil) {
		return in, badInput
	}
	if v.NetMinor != nil {
		net, tax := *v.NetMinor, *v.TaxMinor
		if net < 0 || net > v.Total || tax < 0 || tax != v.Total-net {
			return in, badInput
		}
	}
	v.DueDate = copyDocumentProposalValue(v.DueDate)
	v.NetMinor = copyDocumentProposalValue(v.NetMinor)
	v.TaxMinor = copyDocumentProposalValue(v.TaxMinor)
	return in, nil
}

func (p documentEditPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Preview documentEditPreview
	}{"document_draft_edit.v1", p})
	return sha256.Sum256(raw)
}

func prepareDocumentEdit(r *http.Request, tx *sql.Tx, scope documentReviewScope, input documentEditInput) (documentEditPreview, error) {
	in, err := normalizeDocumentEdit(input)
	if err != nil || scope.Actor <= 0 || scope.Entity <= 0 || scope.Outlet <= 0 {
		return documentEditPreview{}, badInput
	}
	m, err := scopedMembership(tx, scope.Actor, scope.Entity, draftCapability)
	if err != nil {
		return documentEditPreview{}, err
	}
	if err = requireSelectedDocumentOutlet(tx, m, scope.Outlet); err != nil {
		return documentEditPreview{}, err
	}
	p := documentEditPreview{Scope: scope, Input: in}
	var revision int64
	var status string
	var posted bool
	err = tx.QueryRowContext(r.Context(), `SELECT d.issue_date,d.due_date,d.subtotal_minor,d.tax_minor,d.total_minor,
 d.currency,d.revision,d.status,EXISTS(SELECT 1 FROM ap_payables p WHERE p.source_document_id=d.id)
 FROM documents d WHERE d.id=$1 AND d.entity_id=$2 AND d.outlet_id=$3 AND d.deleted_at IS NULL`,
		in.ID, scope.Entity, scope.Outlet).Scan(&p.Before.IssueDate, &p.Before.DueDate, &p.Before.NetMinor, &p.Before.TaxMinor,
		&p.Before.Total, &p.Currency, &revision, &status, &posted)
	if errors.Is(err, sql.ErrNoRows) {
		return documentEditPreview{}, problem{404, "No se encontró el comprobante."}
	}
	if err != nil {
		return documentEditPreview{}, err
	}
	if status != "draft" || posted || revision != in.Revision {
		return documentEditPreview{}, draftRevisionConflict
	}
	before, _ := json.Marshal(p.Before)
	after, _ := json.Marshal(in.After)
	if string(before) == string(after) {
		return documentEditPreview{}, badInput
	}
	return p, nil
}

// The future approval caller must bind the issuing login and pending proposal
// to the same transaction and consume that proposal with the document edit.
func (a *app) applyDocumentEditApproval(r *http.Request, tx *sql.Tx, u user, scope documentReviewScope, p documentEditPreview, approved [32]byte) error {
	if u.id <= 0 || u.id != scope.Actor {
		return unauthenticated
	}
	want := p.digest()
	if p.Scope != scope || subtle.ConstantTimeCompare(want[:], approved[:]) != 1 {
		return draftRevisionConflict
	}
	if err := a.lockDocumentWrite(r, tx, u, scope.Entity); err != nil {
		return err
	}
	if _, err := lockWikiProposalBrowser(r, tx, u.id); err != nil {
		return err
	}
	if err := lockScopedDocumentCapability(r.Context(), tx, scope, draftCapability); err != nil {
		return err
	}
	fresh, err := prepareDocumentEdit(r, tx, scope, p.Input)
	if err != nil {
		return err
	}
	if want != fresh.digest() {
		return draftRevisionConflict
	}
	v := fresh.Input.After
	at := time.Now().UTC().Format(time.RFC3339Nano)
	changed, err := tx.ExecContext(r.Context(), `UPDATE documents SET issue_date=$1,due_date=$2,subtotal_minor=$3,
 tax_minor=$4,total_minor=$5,revision=revision+1,updated_at=$6,updated_by_id=$7
 WHERE id=$8 AND entity_id=$9 AND outlet_id=$10 AND revision=$11 AND status='draft' AND deleted_at IS NULL`,
		v.IssueDate, v.DueDate, v.NetMinor, v.TaxMinor, v.Total, at, scope.Actor, fresh.Input.ID, scope.Entity, scope.Outlet, fresh.Input.Revision)
	if err != nil {
		return err
	}
	return recordNativeDraftUpdate(tx, changed, fresh.Input.ID, scope.Actor, fresh.Input.Reason, at)
}
