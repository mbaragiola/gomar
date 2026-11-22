package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"
)

const documentDraftProposalKind = "document_draft_create.v1"
const documentDraftProposalReason = "Alta de borrador aprobada desde una propuesta de chat"
const maxDocumentDraftMinor = 99999999999999

var (
	draftProposalSupplierMissing = problem{404, "No se encontró un proveedor activo en esta entidad."}
	draftProposalStale           = problem{409, "La propuesta o el proveedor cambiaron. Volvé a preparar el borrador."}
)

// The caller derives actor, entity and outlet from current authorization.
// Never decode them from the model's proposal body.
type documentDraftProposalScope struct {
	Actor  int64
	Entity int64
	Outlet int64
}

type documentDraftProposalInput struct {
	SupplierID int64   `json:"supplier_id"`
	Kind       string  `json:"type"`
	Letter     string  `json:"letter"`
	Point      int64   `json:"point_of_sale"`
	Number     int64   `json:"number"`
	IssueDate  string  `json:"issue_date"`
	DueDate    *string `json:"due_date,omitempty"`
	Currency   string  `json:"currency"`
	Net        *int64  `json:"net_minor,omitempty"`
	Tax        *int64  `json:"tax_minor,omitempty"`
	Total      *int64  `json:"total_minor"`
}

// Browser projection, not an MCP response. Render supplier name as plain text.
type documentDraftProposalPreview struct {
	documentDraftProposalInput
	SupplierName string `json:"supplier_name"`
}

// Private persisted representation for the outer approval workflow. Serialize
// these fields only to the approval store, never a tool or browser response.
type documentDraftProposal struct {
	Scope               documentDraftProposalScope
	Preview             documentDraftProposalPreview
	SupplierFingerprint [32]byte
}

func decodeDocumentDraftProposal(raw []byte) (documentDraftProposalInput, error) {
	var in documentDraftProposalInput
	if len(raw) > 2<<10 {
		return in, badInput
	}
	if err := decodePayables(raw, &in, "supplier_id", "type", "letter", "point_of_sale", "number",
		"issue_date", "due_date", "currency", "net_minor", "tax_minor", "total_minor"); err != nil {
		return in, err
	}
	// Forged unknown fields and repeated keys are both invalid.
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return in, badInput
	}
	seen := map[string]bool{}
	for d.More() {
		next, err := d.Token()
		key, ok := next.(string)
		if err != nil || !ok || seen[key] {
			return in, badInput
		}
		seen[key] = true
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return in, badInput
		}
		if key == "letter" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return in, badInput
		}
	}
	return normalizeDocumentDraftProposal(in)
}

func normalizeDocumentDraftProposal(in documentDraftProposalInput) (documentDraftProposalInput, error) {
	if in.SupplierID <= 0 || in.Point < 1 || in.Point > 99999 || in.Number < 1 || in.Number > 99999999 || in.Total == nil {
		return in, badInput
	}
	if _, ok := documentTypes[in.Kind]; !ok {
		return in, badInput
	}
	in.Letter = strings.ToUpper(trimDraftText(in.Letter))
	if in.Letter != "" && (len(in.Letter) != 1 || in.Letter[0] < 'A' || in.Letter[0] > 'Z') {
		return in, badInput
	}
	if in.Currency != "ARS" && in.Currency != "USD" {
		return in, badInput
	}
	if !documentProposalDate(in.IssueDate) || in.DueDate != nil && !documentProposalDate(*in.DueDate) {
		return in, badInput
	}
	for _, n := range []*int64{in.Net, in.Tax, in.Total} {
		if n != nil && (*n < 0 || *n > maxDocumentDraftMinor) {
			return in, badInput
		}
	}
	// Copy optional values so somebody holding the input DTO cannot edit
	// what the browser is about to review by mutating a pointed-to field.
	in.DueDate = copyDocumentProposalValue(in.DueDate)
	in.Net = copyDocumentProposalValue(in.Net)
	in.Tax = copyDocumentProposalValue(in.Tax)
	in.Total = copyDocumentProposalValue(in.Total)
	return in, nil
}

func copyDocumentProposalValue[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}

func documentProposalDate(value string) bool {
	when, err := time.Parse(time.DateOnly, value)
	return err == nil && when.Year() >= 1
}

func (p documentDraftProposal) digest() [32]byte {
	// Fixed fields, purpose and scope make the lease specific to this
	// operation and actor. The fingerprint does not reveal the CUIT.
	raw, _ := json.Marshal(struct {
		Purpose             string
		Scope               documentDraftProposalScope
		Preview             documentDraftProposalPreview
		SupplierFingerprint [32]byte
	}{documentDraftProposalKind, p.Scope, p.Preview, p.SupplierFingerprint})
	return sha256.Sum256(raw)
}

func lockedDocumentProposalSupplier(ctx context.Context, tx *sql.Tx, entity, id int64) (name, cuit string, fp [32]byte, err error) {
	err = tx.QueryRowContext(ctx, `SELECT name,cuit FROM suppliers
		WHERE entity_id=$1 AND id=$2 AND active FOR SHARE`, entity, id).Scan(&name, &cuit)
	if errors.Is(err, sql.ErrNoRows) {
		err = draftProposalSupplierMissing
	}
	if err == nil {
		raw, _ := json.Marshal(struct{ Name, CUIT string }{name, cuit})
		fp = sha256.Sum256(raw)
	}
	return
}

func documentDraftProposalMember(tx *sql.Tx, s documentDraftProposalScope) (entityMembership, error) {
	m, err := scopedMembership(tx, s.Actor, s.Entity, draftCapability)
	if err == nil {
		err = requireSelectedDocumentOutlet(tx, m, s.Outlet)
	}
	return m, err
}

// Called inside the dedicated proposal intake transaction, after authenticating
// and scoping the proposal-only key. No document (or audit) row is written.
func prepareDocumentDraftProposal(ctx context.Context, tx *sql.Tx, s documentDraftProposalScope, in documentDraftProposalInput) (documentDraftProposal, error) {
	var empty documentDraftProposal
	if s.Actor <= 0 || s.Entity <= 0 || s.Outlet <= 0 {
		return empty, badInput
	}
	in, err := normalizeDocumentDraftProposal(in)
	if err != nil {
		return empty, err
	}
	// Match the native order: take the entity's write lock before any
	// membership/grant/supplier locks. No shared-to-exclusive upgrade.
	if err = lockPostgresEntity(ctx, tx, s.Entity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return empty, missingEntity
		}
		return empty, err
	}
	m, err := documentDraftProposalMember(tx, s)
	if err != nil {
		return empty, err
	}
	name, cuit, fp, err := lockedDocumentProposalSupplier(ctx, tx, s.Entity, in.SupplierID)
	if err != nil {
		return empty, err
	}
	if _, err = validateDraft(tx, m, buildDocumentProposalDraft(s, in, cuit)); err != nil {
		return empty, err
	}
	return documentDraftProposal{s, documentDraftProposalPreview{in, name}, fp}, nil
}

func buildDocumentProposalDraft(s documentDraftProposalScope, in documentDraftProposalInput, cuit string) draftInput {
	money := func(v *int64) *string {
		if v == nil {
			return nil
		}
		amount := minorString(big.NewInt(*v))
		return &amount
	}
	return draftInput{Outlet: &s.Outlet, Supplier: &in.SupplierID, Issuer: &cuit, Kind: &in.Kind, Letter: in.Letter,
		Point: &in.Point, Number: &in.Number, Issue: &in.IssueDate, Due: in.DueDate, Currency: &in.Currency,
		Net: money(in.Net), Tax: money(in.Tax), Total: money(in.Total)}
}

// Call only after same-actor browser CSRF/confirmation and proposal checks.
// The caller must record the terminal receipt in this transaction before
// committing. We use the native draft writer and never review or post it.
func (a *app) applyDocumentDraftProposal(r *http.Request, tx *sql.Tx, u user, s documentDraftProposalScope, p documentDraftProposal, confirmedDigest [32]byte) (int64, error) {
	if u.id == 0 || s.Actor != u.id {
		return 0, unauthenticated
	}
	if p.Scope != s {
		return 0, draftProposalStale
	}
	digest := p.digest()
	if subtle.ConstantTimeCompare(digest[:], confirmedDigest[:]) != 1 {
		return 0, draftProposalStale
	}
	in, err := normalizeDocumentDraftProposal(p.Preview.documentDraftProposalInput)
	if err != nil {
		return 0, draftProposalStale
	}
	// The workflow must reserve the entity lock ahead of any shared scope
	// locks; lockDocumentWrite also rechecks the live browser session.
	if err = a.lockDocumentWrite(r, tx, u, s.Entity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, missingEntity
		}
		return 0, err
	}
	m, err := documentDraftProposalMember(tx, s)
	if err != nil {
		return 0, err
	}
	_, cuit, fp, err := lockedDocumentProposalSupplier(r.Context(), tx, s.Entity, in.SupplierID)
	if err == draftProposalSupplierMissing {
		return 0, draftProposalStale
	}
	if err != nil {
		return 0, err
	}
	if subtle.ConstantTimeCompare(fp[:], p.SupplierFingerprint[:]) != 1 {
		return 0, draftProposalStale
	}
	return insertDraft(tx, m, buildDocumentProposalDraft(s, in, cuit), documentDraftProposalReason)
}
