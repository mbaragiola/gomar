package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"
)

var documentNumberConflict = problem{409, "No se pudo aplicar la corrección. Verificá el borrador y el número en Gomar."}

const documentNumberAuditReason = "Corrección del número del comprobante confirmada en Gomar"

type documentNumberInput struct {
	ID       int64 `json:"document_id"`
	Revision int64 `json:"expected_revision"`
	Number   int64 `json:"number"`
}

func (in documentNumberInput) valid() bool {
	return in.ID > 0 && in.Revision > 0 && in.Revision < math.MaxInt64 && in.Number > 0 && in.Number <= 99_999_999
}

type documentNumberPreview struct {
	Scope    documentReviewScope
	Input    documentNumberInput
	Current  string
	Proposed string
}

func (p documentNumberPreview) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Preview documentNumberPreview
	}{documentNumberProposalPurpose, p})
	return sha256.Sum256(raw)
}

func prepareDocumentNumber(r *http.Request, tx *sql.Tx, scope documentReviewScope, in documentNumberInput) (documentNumberPreview, error) {
	if !in.valid() {
		return documentNumberPreview{}, badInput
	}
	p := documentNumberPreview{Scope: scope, Input: in}
	var revision, point, current int64
	var kind, letter, status, issuer string
	var posted, matchingSupplier bool
	err := tx.QueryRowContext(r.Context(), `SELECT d.revision,d.document_type,d.letter,d.point_of_sale,d.number,d.status,d.issuer_cuit,
        EXISTS(SELECT 1 FROM ap_payables p WHERE p.source_document_id=d.id),
        EXISTS(SELECT 1 FROM suppliers s WHERE s.id=d.supplier_id AND s.entity_id=d.entity_id AND s.active AND s.cuit=d.issuer_cuit)
        FROM documents d WHERE d.id=$1 AND d.entity_id=$2 AND d.outlet_id=$3 AND d.deleted_at IS NULL`,
		in.ID, scope.Entity, scope.Outlet).Scan(&revision, &kind, &letter, &point, &current, &status, &issuer, &posted, &matchingSupplier)
	if errors.Is(err, sql.ErrNoRows) {
		return p, mcpMissingDocument
	}
	if err != nil {
		return p, err
	}
	if canonical, err := normalizeCUIT(issuer); err != nil || canonical != issuer {
		return p, documentNumberConflict
	}
	if revision != in.Revision || status != "draft" || posted || !matchingSupplier {
		return p, documentNumberConflict
	}
	if current == in.Number {
		return p, badInput
	}
	label := documentTypes[kind]
	// The narrow action cannot repair legacy identity data in other columns.
	if label == "" || current < 1 || current > 99_999_999 || point < 1 || point > 99_999 ||
		(letter != "" && (len(letter) != 1 || letter[0] < 'A' || letter[0] > 'Z')) {
		return p, documentNumberConflict
	}
	if letter != "" {
		label += " " + letter
	}
	p.Current = fmt.Sprintf("%s %05d-%08d", label, point, current)
	p.Proposed = fmt.Sprintf("%s %05d-%08d", label, point, in.Number)
	// Do not check target uniqueness on the MCP channel. The database
	// constraint settles it only when the issuing browser approves.
	return p, nil
}

func (a *app) applyDocumentNumberApproval(r *http.Request, tx *sql.Tx, u user, scope documentReviewScope, p documentNumberPreview, approved [32]byte) error {
	want := p.digest()
	if u.id <= 0 || u.id != scope.Actor || p.Scope != scope || subtle.ConstantTimeCompare(want[:], approved[:]) != 1 {
		return wikiProposalConflict
	}
	// beginDocumentNumberReview already holds the browser/session, native
	// entity and selected grant locks, in that order for the whole decision.
	fresh, err := prepareDocumentNumber(r, tx, scope, p.Input)
	if err != nil {
		return err
	}
	if fresh.digest() != want {
		return documentNumberConflict
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	changed, err := tx.ExecContext(r.Context(), `UPDATE documents SET number=$1,revision=revision+1,updated_at=$2,updated_by_id=$3
        WHERE id=$4 AND entity_id=$5 AND outlet_id=$6 AND revision=$7 AND status='draft' AND deleted_at IS NULL`,
		p.Input.Number, at, scope.Actor, p.Input.ID, scope.Entity, scope.Outlet, p.Input.Revision)
	if isDocumentIdentityConflict(err) {
		// Do not disclose whether a conflicting row is in this branch.
		return documentNumberConflict
	}
	if err != nil {
		return err
	}
	return recordNativeDraftUpdate(tx, changed, p.Input.ID, scope.Actor, documentNumberAuditReason, at)
}
