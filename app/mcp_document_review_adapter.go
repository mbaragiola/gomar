package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"
)

type documentReviewScope struct{ Actor, Entity, Outlet int64 }
type documentReviewInput struct {
	ID       int64  `json:"document_id"`
	Revision int64  `json:"expected_revision"`
	Reason   string `json:"reason"`
}
type documentReviewSnapshot struct {
	Scope      documentReviewScope
	Input      documentReviewInput
	Type       string
	Currency   string
	TotalMinor int64
}

func normalizeDocumentReview(in documentReviewInput) (documentReviewInput, error) {
	in.Reason = trimDraftText(in.Reason)
	if in.ID < 1 || in.Revision < 1 || in.Revision == 1<<63-1 || !utf8.ValidString(in.Reason) ||
		in.Reason == "" || len(in.Reason) > 1024 || strings.ContainsRune(in.Reason, 0) {
		return in, badInput
	}
	return in, nil
}
func (p documentReviewSnapshot) digest() [32]byte {
	data, _ := json.Marshal(struct {
		Purpose  string
		Snapshot documentReviewSnapshot
	}{"document_review.v1", p})
	return sha256.Sum256(data)
}

// prepareDocumentReview takes actor and scope from the future authorized
// caller. It does not write or return supplier details, files or contacts.
func prepareDocumentReview(r *http.Request, tx *sql.Tx, scope documentReviewScope, input documentReviewInput) (documentReviewSnapshot, error) {
	in, err := normalizeDocumentReview(input)
	if err != nil || scope.Actor < 1 || scope.Entity < 1 || scope.Outlet < 1 {
		return documentReviewSnapshot{}, badInput
	}
	m, err := scopedMembership(tx, scope.Actor, scope.Entity, "review_documents")
	if err != nil {
		return documentReviewSnapshot{}, err
	}
	if err = requireSelectedDocumentOutlet(tx, m, scope.Outlet); err != nil {
		return documentReviewSnapshot{}, err
	}
	var state string
	var revision int64
	p := documentReviewSnapshot{Scope: scope, Input: in}
	err = tx.QueryRowContext(r.Context(), `SELECT document_type,currency,total_minor,status,revision FROM documents
 WHERE entity_id=$1 AND outlet_id=$2 AND id=$3 AND deleted_at IS NULL`,
		scope.Entity, scope.Outlet, in.ID).Scan(&p.Type, &p.Currency, &p.TotalMinor, &state, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return documentReviewSnapshot{}, problem{404, "No se encontró el comprobante."}
	}
	if err != nil {
		return documentReviewSnapshot{}, err
	}
	if state != "draft" || revision != in.Revision {
		return documentReviewSnapshot{}, draftRevisionConflict
	}
	return p, nil
}

// applyDocumentReviewApproval is not an HTTP handler. Only a future browser
// approval workflow may call it, in the transaction that records its decision.
func (a *app) applyDocumentReviewApproval(r *http.Request, tx *sql.Tx, u user, scope documentReviewScope, p documentReviewSnapshot, approval [32]byte) error {
	if u.id == 0 || u.id != scope.Actor {
		return unauthenticated
	}
	digest := p.digest()
	if p.Scope != scope || subtle.ConstantTimeCompare(digest[:], approval[:]) != 1 {
		return draftRevisionConflict
	}
	if err := a.lockDocumentWrite(r, tx, u, scope.Entity); err != nil {
		return err
	}
	fresh, err := prepareDocumentReview(r, tx, scope, p.Input)
	if err != nil {
		return err
	}
	if fresh != p {
		return draftRevisionConflict
	}
	m, err := scopedMembership(tx, u.id, scope.Entity, "review_documents")
	if err != nil {
		return err
	}
	return applyNativeDraftReview(tx, m, p.Input.ID, p.Input.Revision, p.Input.Reason)
}

const documentReviewSessionAction = "document_draft_review"
const documentReviewProposalPurpose = "document_review.v1"

func (a *app) documentReviewProposalsEnabled() bool {
	return a.enableMCPDocumentReviewProposals
}

func decodeDocumentReview(raw []byte) (documentReviewInput, error) {
	var in documentReviewInput
	if len(raw) > 2048 {
		return in, badInput
	}
	if err := decodeWikiProposalJSON(raw, &in, "document_id", "expected_revision", "reason"); err != nil {
		return in, err
	}
	return normalizeDocumentReview(in)
}

func lockDocumentReviewScope(ctx context.Context, tx *sql.Tx, s documentReviewScope) error {
	var member int64
	var role string
	err := tx.QueryRowContext(ctx, `SELECT m.id,m.role FROM memberships m JOIN entities e ON e.id=m.entity_id AND e.active
 WHERE m.entity_id=$1 AND m.user_id=$2 AND m.active FOR SHARE OF m,e`, s.Entity, s.Actor).Scan(&member, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return missingEntity
	}
	if err != nil {
		return err
	}
	if !slices.Contains(capabilities[role], "review_documents") {
		return forbiddenRole
	}
	var outlet int64
	err = tx.QueryRowContext(ctx, `SELECT o.id FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id AND g.membership_id=$1
 WHERE o.entity_id=$2 AND o.id=$3 AND o.active FOR SHARE OF o,g`, member, s.Entity, s.Outlet).Scan(&outlet)
	if errors.Is(err, sql.ErrNoRows) {
		return missingOutlet
	}
	return err
}
