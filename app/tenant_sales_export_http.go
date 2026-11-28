package main

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"

	"github.com/mbaragiola/gomar/app/internal/mcpclient"
)

const tenantSalesExportPagePath = "/exportaciones/ventas/{entity}/{outlet}"

type tenantSalesExportPage struct {
	Entity, Outlet, CSRF, RequestID, From, Through, Digest string
	Acknowledged                                           bool
}

func (a *app) registerTenantSalesExport(add func(string, string, http.HandlerFunc)) {
	auth := a.protectedWithCache(a.tenantSalesExportBrowser, true)
	add(tenantSalesExportPagePath, http.MethodGet, auth)
	add(tenantSalesExportPagePath, http.MethodPost, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			failure(w, badInput)
			return
		}
		auth(w, r)
	})
}

func (a *app) tenantSalesExportBrowser(w http.ResponseWriter, r *http.Request, u user) error {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || len(r.Header.Values("Authorization")) != 0 {
		return badInput
	}
	entity, entityOK := attachmentHandoffID(r, "entity")
	outlet, outletOK := attachmentHandoffID(r, "outlet")
	if !entityOK || !outletOK {
		return missingOutlet
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entity, tenantSalesExportCapability)
	if err != nil {
		return err
	}
	if err := a.requireTenantSalesExportOutlet(r, tx, member, outlet); err != nil {
		return err
	}
	page := tenantSalesExportPage{Entity: member.Name}
	if err := tx.QueryRowContext(ctx, "SELECT name FROM outlets WHERE entity_id=$1 AND id=$2", entity, outlet).Scan(&page.Outlet); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	page.CSRF = ensureCSRF(w, r)
	if r.Method != http.MethodPost {
		var id [16]byte
		rand.Read(id[:])
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		page.RequestID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
		return page.render(w)
	}

	for _, values := range r.PostForm {
		if len(values) != 1 {
			return badInput
		}
	}
	page.RequestID, page.From, page.Through = r.PostForm.Get("request_id"), r.PostForm.Get("from"), r.PostForm.Get("through")
	scope := tenantSalesExportScope{entity: entity, outlet: outlet, requestID: page.RequestID}
	action := r.PostForm.Get("action")
	switch action {
	case "prepare", "download":
		result, err := a.prepareTenantSalesExport(r, u, scope, page.From, page.Through)
		if err != nil {
			return err
		}
		if action == "download" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="ventas.csv"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(result.CSV)))
			w.Write(result.CSV)
			return nil
		}
		page.Digest, page.Acknowledged = result.Digest, result.Acknowledged
	case "acknowledge":
		if r.PostForm.Get("received") != "yes" {
			return badInput
		}
		if err := a.acknowledgeTenantSalesExport(r, u, scope, r.PostForm.Get("digest")); err != nil {
			return err
		}
		page.Acknowledged = true
	default:
		return badInput
	}
	return page.render(w)
}

func (a *app) mcpTenantSalesExportHandoff(r *http.Request, tx *sql.Tx, s mcpTokenScope) (any, error) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
		return nil, badInput
	}
	outlet, ok := attachmentHandoffID(r, "outlet")
	if !ok || !s.outlet.Valid || outlet != s.outlet.Int64 {
		return nil, missingOutlet
	}
	if !slices.Contains(capabilities[s.role], tenantSalesExportCapability) {
		return nil, forbiddenRole
	}
	if err := a.requireTenantSalesExportOutlet(r, tx, s.entityMembership, outlet); err != nil {
		return nil, err
	}
	return mcpclient.TenantSalesExportHandoff{HandoffURL: mcpclient.TenantSalesExportURL(s.ID, outlet)}, nil
}

func (page tenantSalesExportPage) render(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	return tenantSalesExportTemplate.Execute(w, page)
}

//go:embed tenant_sales_export.html
var tenantSalesExportTemplates embed.FS

var tenantSalesExportTemplate = template.Must(template.ParseFS(tenantSalesExportTemplates, "tenant_sales_export.html"))
