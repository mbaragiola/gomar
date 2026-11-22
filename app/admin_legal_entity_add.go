package main

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const adminLegalEntityAddPath = adminLegalEntityPath + "add/"

func (a *app) serveAdminLegalEntityAdd(w http.ResponseWriter, r *http.Request, page adminPage, actor int64) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fields := adminCatalogAddPage{Active: true, Legal: true, FormID: "legalentity_form", FormPath: adminLegalEntityAddPath,
		NameLabel: "Razón social", NameField: "name", MaxName: 160}
	if r.Method == http.MethodPost && a.addAdminLegalEntity(w, r, actor, &fields) {
		return
	}
	page.Title = "Agregar entidad legal"
	page.CatalogAdd = &fields
	renderAdmin(w, page)
}

func (a *app) addAdminLegalEntity(w http.ResponseWriter, r *http.Request, actor int64, fields *adminCatalogAddPage) bool {
	for key, values := range r.PostForm {
		switch key {
		case "csrfmiddlewaretoken", "name", "active", "cuit", "_save":
			if len(values) == 1 {
				continue
			}
		}
		http.Error(w, "Formulario de entidad no compatible.", http.StatusBadRequest)
		return true
	}
	fields.Name = strings.TrimSpace(r.PostForm.Get("name"))
	fields.CUIT = strings.TrimSpace(r.PostForm.Get("cuit"))
	fields.Active = r.PostForm.Get("active") == "on"
	cuit, err := normalizeCUIT(fields.CUIT)
	switch {
	case fields.Name == "":
		fields.Problem = "Este campo es obligatorio."
		return false
	case !utf8.ValidString(fields.Name) || utf8.RuneCountInString(fields.Name) > 160:
		fields.Problem = "La razón social no puede superar 160 caracteres."
		return false
	case err != nil || cuit != fields.CUIT:
		fields.Problem = "Ingresá un CUIT válido de 11 dígitos."
		return false
	case r.PostForm.Has("active") && !fields.Active:
		http.Error(w, "Estado de entidad no compatible.", http.StatusBadRequest)
		return true
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		failure(w, err)
		return true
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, "INSERT INTO entities(name,cuit,active) VALUES($1,$2,$3) RETURNING id", fields.Name, cuit, fields.Active).Scan(&id)
	if isUnique(err) {
		fields.Problem = "Ya existe una entidad con ese CUIT."
		return false
	}
	if err != nil {
		failure(w, err)
		return true
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO admin_legal_entity_history(entity_id,actor_id,occurred_at) VALUES($1,$2,$3)",
			id, actor, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		failure(w, err)
		return true
	}
	http.Redirect(w, r, adminLegalEntityPath, http.StatusFound)
	return true
}
