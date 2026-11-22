package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const knowledgePath = "/api/v1/entities/{entity}/outlets/{outlet}/knowledge"
const knowledgeColumns = "id,title,body,version,created_at,updated_at"

var missingKnowledge = problem{404, "No se encontró el artículo."}
var staleKnowledge = problem{409, "El artículo cambió. Volvé a abrirlo antes de guardar."}

type knowledgeArticle struct {
	ID        int64  `json:"id"`
	Title     string `json:"titulo"`
	Body      string `json:"contenido"`
	Version   int64  `json:"version"`
	CreatedAt string `json:"creado_en"`
	UpdatedAt string `json:"actualizado_en"`
}

func (a *app) registerKnowledge(add func(string, string, http.HandlerFunc)) {
	add(knowledgePath, http.MethodGet, a.scoped("read_business_data", listKnowledge))
	add(knowledgePath, http.MethodPost, a.scopedRequest("manage_entity_access", writeKnowledge, true))
	add(knowledgePath+"/{article}", http.MethodGet, a.scoped("read_business_data", viewKnowledge))
	add(knowledgePath+"/{article}", http.MethodPut, a.scopedRequest("manage_entity_access", writeKnowledge, true))
}

func knowledgeOutlet(r *http.Request, tx *sql.Tx, m entityMembership) (int64, error) {
	id, err := pathID(r, "outlet")
	if err != nil {
		return 0, err
	}
	return id, requireSelectedDocumentOutlet(tx, m, id)
}

func scanKnowledge(row interface{ Scan(...any) error }) (knowledgeArticle, error) {
	var a knowledgeArticle
	err := row.Scan(&a.ID, &a.Title, &a.Body, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = missingKnowledge
	}
	return a, err
}

func listKnowledge(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	outlet, err := knowledgeOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || (len(query) != 0 && (len(query) != 1 || len(query["q"]) != 1)) {
		return 0, nil, badInput
	}
	q := strings.TrimSpace(query.Get("q"))
	if !utf8.ValidString(q) || utf8.RuneCountInString(q) > 120 || strings.ContainsRune(q, 0) {
		return 0, nil, badInput
	}
	q = strings.ToLower(q)
	rows, err := tx.QueryContext(r.Context(), "SELECT "+knowledgeColumns+` FROM knowledge_articles
		WHERE outlet_id=$1 AND ($2='' OR replace(search_text,$2,'')<>search_text) ORDER BY id DESC LIMIT 50`, outlet, q)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	articles := make([]knowledgeArticle, 0)
	for rows.Next() {
		a, err := scanKnowledge(rows)
		if err != nil {
			return 0, nil, err
		}
		articles = append(articles, a)
	}
	return 200, struct {
		Articles []knowledgeArticle `json:"articulos"`
	}{articles}, rows.Err()
}

func viewKnowledge(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	outlet, err := knowledgeOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	id, err := pathID(r, "article")
	if err != nil {
		return 0, nil, err
	}
	a, err := scanKnowledge(tx.QueryRowContext(r.Context(), "SELECT "+knowledgeColumns+
		" FROM knowledge_articles WHERE outlet_id=$1 AND id=$2", outlet, id))
	return 200, a, err
}

func writeKnowledge(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	ctx := r.Context()
	outlet, err := knowledgeOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	var input struct {
		Title   string `json:"titulo"`
		Body    string `json:"contenido"`
		Version int64  `json:"version"`
	}
	create := r.Method == http.MethodPost
	fields := []string{"titulo", "contenido"}
	if !create {
		fields = append(fields, "version")
	}
	if err := decodePayables(raw, &input, fields...); err != nil {
		return 0, nil, err
	}
	title, err := normalizeKnowledgeText(input.Title, input.Body)
	if err != nil {
		return 0, nil, err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	search := strings.ToLower(title + "\n" + input.Body)
	var stored knowledgeArticle
	status := 200
	if create {
		status = 201
		stored, err = scanKnowledge(tx.QueryRowContext(ctx, `INSERT INTO knowledge_articles
			(outlet_id,title,body,search_text,version,created_at,updated_at)
			VALUES($1,$2,$3,$4,1,$5,$5) RETURNING `+knowledgeColumns, outlet, title, input.Body, search, stamp))
	} else {
		id, parseErr := pathID(r, "article")
		if parseErr != nil {
			return 0, nil, parseErr
		}
		if input.Version < 1 {
			return 0, nil, badInput
		}
		current, readErr := scanKnowledge(tx.QueryRowContext(ctx, "SELECT "+knowledgeColumns+
			" FROM knowledge_articles WHERE outlet_id=$1 AND id=$2", outlet, id))
		if readErr != nil {
			return 0, nil, readErr
		}
		if current.Version != input.Version {
			return 0, nil, staleKnowledge
		}
		stored, err = scanKnowledge(tx.QueryRowContext(ctx, `UPDATE knowledge_articles
			SET title=$1,body=$2,search_text=$3,version=version+1,updated_at=$4
			WHERE outlet_id=$5 AND id=$6 AND version=$7 RETURNING `+knowledgeColumns,
			title, input.Body, search, stamp, outlet, id, input.Version))
		if err == missingKnowledge {
			return 0, nil, staleKnowledge
		}
	}
	if err != nil {
		return 0, nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_article_versions
		(article_id,version,actor_id,recorded_at) VALUES($1,$2,$3,$4)`, stored.ID, stored.Version, m.userID, stamp)
	return status, stored, err
}

func normalizeKnowledgeText(rawTitle, body string) (string, error) {
	title := strings.TrimSpace(rawTitle)
	if title == "" || strings.TrimSpace(body) == "" || utf8.RuneCountInString(title) > 120 ||
		utf8.RuneCountInString(body) > 12000 || len(body) > 48<<10 ||
		strings.ContainsRune(title, 0) || strings.ContainsRune(body, 0) {
		return "", badInput
	}
	return title, nil
}
