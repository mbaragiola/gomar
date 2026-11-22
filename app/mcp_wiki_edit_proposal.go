package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

const wikiEditProposalPurpose = "wiki_edit.v1"
const wikiEditCapability = "manage_entity_access"

var wikiEditNoChange = problem{422, "No hay cambios en el artículo."}

type wikiEditScope struct {
	Actor  int64
	Entity int64
	Outlet int64
}

type wikiEditInput struct {
	ArticleID int64  `json:"article_id"`
	Version   int64  `json:"expected_version"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

type wikiEditText struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Only the authenticated browser reviewer should see a preview. Both sides
// are inert text, not executable markup or instructions.
type wikiEditPreview struct {
	ArticleID int64        `json:"article_id"`
	Version   int64        `json:"expected_version"`
	Before    wikiEditText `json:"before"`
	After     wikiEditText `json:"after"`
}

type wikiEditProposal struct {
	Scope   wikiEditScope
	Preview wikiEditPreview
}

func decodeWikiEditProposal(raw []byte) (wikiEditInput, error) {
	var in wikiEditInput
	if len(raw) > 64<<10 {
		return in, badInput
	}
	if err := decodePayables(raw, &in, "article_id", "expected_version", "title", "body"); err != nil {
		return in, err
	}
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
		var field json.RawMessage
		if err = d.Decode(&field); err != nil {
			return in, badInput
		}
	}
	return normalizeWikiEditProposal(in)
}

func normalizeWikiEditProposal(in wikiEditInput) (wikiEditInput, error) {
	if in.ArticleID <= 0 || in.Version <= 0 || in.Version == 1<<63-1 {
		return in, badInput
	}
	title, err := normalizeKnowledgeText(in.Title, in.Body)
	if err != nil {
		return in, err
	}
	in.Title = title
	return in, nil
}

func (p wikiEditProposal) digest() [32]byte {
	raw, _ := json.Marshal(struct {
		Purpose string
		Scope   wikiEditScope
		Preview wikiEditPreview
	}{wikiEditProposalPurpose, p.Scope, p.Preview})
	return sha256.Sum256(raw)
}

func wikiEditProposalMember(ctx context.Context, tx *sql.Tx, s wikiEditScope) (entityMembership, error) {
	if s.Actor <= 0 || s.Entity <= 0 || s.Outlet <= 0 {
		return entityMembership{}, badInput
	}
	if err := lockPostgresEntity(ctx, tx, s.Entity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entityMembership{}, missingEntity
		}
		return entityMembership{}, err
	}
	m, err := scopedMembership(tx, s.Actor, s.Entity, wikiEditCapability)
	if err == nil {
		err = requireSelectedDocumentOutlet(tx, m, s.Outlet)
	}
	return m, err
}

func prepareWikiEditProposal(ctx context.Context, tx *sql.Tx, s wikiEditScope, in wikiEditInput) (wikiEditProposal, error) {
	var empty wikiEditProposal
	in, err := normalizeWikiEditProposal(in)
	if err != nil {
		return empty, err
	}
	if _, err = wikiEditProposalMember(ctx, tx, s); err != nil {
		return empty, err
	}
	item, err := scanKnowledge(tx.QueryRowContext(ctx, "SELECT "+knowledgeColumns+
		" FROM knowledge_articles WHERE outlet_id=$1 AND id=$2", s.Outlet, in.ArticleID))
	if err != nil {
		return empty, err
	}
	if item.Version != in.Version {
		return empty, staleKnowledge
	}
	if _, err = normalizeKnowledgeText(item.Title, item.Body); err != nil {
		return empty, staleKnowledge
	}
	before, after := wikiEditText{item.Title, item.Body}, wikiEditText{in.Title, in.Body}
	if before == after {
		return empty, wikiEditNoChange
	}
	return wikiEditProposal{s, wikiEditPreview{item.ID, item.Version, before, after}}, nil
}

// Only called after explicit same-actor browser approval of the exact
// preview. The caller owns CSRF, purpose, lifetime, replay and terminal
// receipt. It must write that receipt in this transaction before committing.
func (a *app) applyWikiEditProposal(r *http.Request, tx *sql.Tx, u user, s wikiEditScope, p wikiEditProposal, confirmed [32]byte) (knowledgeArticle, error) {
	var empty knowledgeArticle
	if u.id == 0 || s.Actor != u.id {
		return empty, unauthenticated
	}
	if p.Scope != s {
		return empty, staleKnowledge
	}
	digest := p.digest()
	if subtle.ConstantTimeCompare(digest[:], confirmed[:]) != 1 {
		return empty, staleKnowledge
	}
	input, err := normalizeWikiEditProposal(wikiEditInput{p.Preview.ArticleID, p.Preview.Version, p.Preview.After.Title, p.Preview.After.Body})
	if err != nil {
		return empty, staleKnowledge
	}

	m, err := wikiEditProposalMember(r.Context(), tx, s)
	if err != nil {
		return empty, err
	}
	active, err := currentUser(nil, r, tx)
	if err != nil {
		return empty, err
	}
	if active.id != u.id {
		return empty, unauthenticated
	}
	current, err := scanKnowledge(tx.QueryRowContext(r.Context(), "SELECT "+knowledgeColumns+
		" FROM knowledge_articles WHERE outlet_id=$1 AND id=$2 FOR UPDATE", s.Outlet, input.ArticleID))
	if err != nil {
		return empty, err
	}
	if current.Version != input.Version || (wikiEditText{current.Title, current.Body}) != p.Preview.Before {
		return empty, staleKnowledge
	}

	// Reuse the existing native wiki PUT transaction, scoped exclusively from
	// verified IDs. Native version CAS and history write stay authoritative.
	write := r.Clone(r.Context())
	write.Method = http.MethodPut
	write.SetPathValue("outlet", strconv.FormatInt(s.Outlet, 10))
	write.SetPathValue("article", strconv.FormatInt(input.ArticleID, 10))
	raw, err := json.Marshal(struct {
		Title   string `json:"titulo"`
		Body    string `json:"contenido"`
		Version int64  `json:"version"`
	}{input.Title, input.Body, input.Version})
	if err != nil {
		return empty, err
	}
	_, result, err := writeKnowledge(write, tx, m, raw)
	if err != nil {
		return empty, err
	}
	return result.(knowledgeArticle), nil
}
