package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type adminCatalogFields struct {
	Entity, Name, CUIT, NameLabel, ListPath, ListLabel string
	Active, SupplierEdit                               bool
	Action, Problem, HistoryPath, Role                 string
}

func hasAdminCatalogDetail(path string) bool {
	return path == adminOutletPath || path == adminLegalEntityPath || path == adminSupplierPath || path == adminMembershipPath
}

func adminCatalogDetailID(path string) (string, int64, bool) {
	for _, list := range []string{adminOutletPath, adminLegalEntityPath, adminSupplierPath, adminMembershipPath} {
		raw, ok := strings.CutPrefix(path, list)
		if !ok {
			continue
		}
		raw, ok = strings.CutSuffix(raw, "/change/")
		if !ok || raw == "" || strings.Trim(raw, "0123456789") != "" {
			return "", 0, false
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		return list, id, err == nil
	}
	return "", 0, false
}

func (a *app) serveAdminCatalogDetail(w http.ResponseWriter, r *http.Request, page adminPage, list string, id, actorID int64) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !((list == adminOutletPath || list == adminSupplierPath) && r.Method == http.MethodPost) {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fields := adminCatalogFields{ListPath: list, NameLabel: "Razón social"}
	var err error
	switch list {
	case adminOutletPath:
		fields.Action = adminOutletPath + strconv.FormatInt(id, 10) + "/change/"
		fields.HistoryPath = adminOutletPath + strconv.FormatInt(id, 10) + "/history/"
		page.Title, fields.ListLabel, fields.NameLabel = "Cambiar local", "locales", "Nombre"
		err = a.db.QueryRowContext(r.Context(), `SELECT e.name,o.name,o.active FROM outlets o
			JOIN entities e ON e.id=o.entity_id WHERE o.id=$1`, id).Scan(&fields.Entity, &fields.Name, &fields.Active)
	case adminLegalEntityPath:
		page.Title, fields.ListLabel = "Ver entidad legal", "entidades legales"
		fields.HistoryPath = adminLegalEntityPath + strconv.FormatInt(id, 10) + "/history/"
		err = a.db.QueryRowContext(r.Context(), `SELECT name,cuit,active FROM entities WHERE id=$1`, id).
			Scan(&fields.Name, &fields.CUIT, &fields.Active)
	case adminSupplierPath:
		page.Title, fields.ListLabel = "Cambiar proveedor", "proveedores"
		fields.SupplierEdit = true
		fields.Action = adminSupplierPath + strconv.FormatInt(id, 10) + "/change/"
		fields.HistoryPath = adminSupplierPath + strconv.FormatInt(id, 10) + "/history/"
		err = a.db.QueryRowContext(r.Context(), `SELECT e.name,s.name,s.cuit,s.active FROM suppliers s
			JOIN entities e ON e.id=s.entity_id WHERE s.id=$1`, id).
			Scan(&fields.Entity, &fields.Name, &fields.CUIT, &fields.Active)
	case adminMembershipPath:
		page.Title, fields.ListLabel, fields.NameLabel = "Ver membresía", "membresías", "Usuario"
		var role string
		err = a.db.QueryRowContext(r.Context(), `SELECT e.name,u.username,m.role,m.active
			FROM memberships m JOIN entities e ON e.id=m.entity_id JOIN users u ON u.id=m.user_id
			WHERE m.id=$1`, id).Scan(&fields.Entity, &fields.Name, &role, &fields.Active)
		fields.Role = roleLabel(role)
	default:
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	if r.Method == http.MethodPost {
		if fields.SupplierEdit {
			if a.saveAdminSupplier(w, r, id, actorID, &fields) {
				return
			}
		} else if a.saveAdminOutlet(w, r, id, actorID, &fields) {
			return
		}
	}
	page.CatalogDetail = &fields
	renderAdmin(w, page)
}

// saveAdminOutlet accepts the ordinary existing-outlet form only. Entity,
// grants and every other relation are outside this write statement.
func (a *app) saveAdminOutlet(w http.ResponseWriter, r *http.Request, id, actorID int64, fields *adminCatalogFields) bool {
	for key, values := range r.PostForm {
		switch key {
		case "csrfmiddlewaretoken", "name", "active", "_save":
			if len(values) == 1 {
				continue
			}
		}
		http.Error(w, "Formulario de local no compatible.", http.StatusBadRequest)
		return true
	}
	fields.Name = strings.TrimSpace(r.PostForm.Get("name"))
	fields.Active = r.PostForm.Get("active") == "on"
	switch {
	case fields.Name == "":
		fields.Problem = "Este campo es obligatorio."
		return false
	case !utf8.ValidString(fields.Name) || utf8.RuneCountInString(fields.Name) > 120:
		fields.Problem = "El nombre del local no puede superar 120 caracteres."
		return false
	case r.PostForm.Has("active") && !fields.Active:
		http.Error(w, "Estado de local no compatible.", http.StatusBadRequest)
		return true
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		failure(w, err)
		return true
	}
	defer tx.Rollback()
	var name string
	var active bool
	var entityID int64
	err = tx.QueryRowContext(ctx, "SELECT entity_id FROM outlets WHERE id=$1", id).Scan(&entityID)
	if err == nil {
		err = lockPostgresEntity(ctx, tx, entityID)
	}
	if err == nil {
		err = lockPostgresOutlet(ctx, tx, id)
	}
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT name,active FROM outlets WHERE id=$1", id).Scan(&name, &active)
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return true
	}
	if err != nil {
		failure(w, err)
		return true
	}
	_, err = tx.ExecContext(ctx, "UPDATE outlets SET name=$1,active=$2 WHERE id=$3", fields.Name, fields.Active, id)
	if isUnique(err) {
		fields.Problem = "Ya existe un local con ese nombre en la entidad."
		return false
	}
	if err != nil {
		failure(w, err)
		return true
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_outlet_history(outlet_id,actor_id,occurred_at,name_changed,active_changed)
		VALUES($1,$2,$3,$4,$5)`, id, actorID, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"), name != fields.Name, active != fields.Active)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		failure(w, err)
		return true
	}
	http.Redirect(w, r, adminOutletPath, http.StatusFound)
	return true
}

// saveAdminSupplier accepts the ordinary name and active fields, not CUIT or owner.
func (a *app) saveAdminSupplier(w http.ResponseWriter, r *http.Request, id, actorID int64, fields *adminCatalogFields) bool {
	for key, values := range r.PostForm {
		switch key {
		case "csrfmiddlewaretoken", "business_name", "active", "_save":
			if len(values) == 1 {
				continue
			}
		}
		http.Error(w, "Formulario de proveedor no compatible.", http.StatusBadRequest)
		return true
	}
	fields.Name = strings.TrimSpace(r.PostForm.Get("business_name"))
	fields.Active = r.PostForm.Get("active") == "on"
	if r.PostForm.Has("active") && !fields.Active {
		http.Error(w, "Estado de proveedor no compatible.", http.StatusBadRequest)
		return true
	}
	if fields.Name == "" {
		fields.Problem = "Este campo es obligatorio."
		return false
	}
	if !utf8.ValidString(fields.Name) || utf8.RuneCountInString(fields.Name) > 160 {
		fields.Problem = "La razón social no puede superar 160 caracteres."
		return false
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		failure(w, err)
		return true
	}
	defer tx.Rollback()
	var prior string
	var active bool
	var entityID int64
	err = tx.QueryRowContext(ctx, "SELECT entity_id FROM suppliers WHERE id=$1", id).Scan(&entityID)
	if err == nil {
		err = lockPostgresEntity(ctx, tx, entityID)
	}
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT name,active FROM suppliers WHERE id=$1", id).Scan(&prior, &active)
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return true
	}
	if err != nil {
		failure(w, err)
		return true
	}
	_, err = tx.ExecContext(ctx, "UPDATE suppliers SET name=$1,active=$2 WHERE id=$3", fields.Name, fields.Active, id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO admin_supplier_history(supplier_id,actor_id,occurred_at,name_changed,active_changed)
			VALUES($1,$2,$3,$4,$5)`, id, actorID, time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"), prior != fields.Name, active != fields.Active)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		failure(w, err)
		return true
	}
	http.Redirect(w, r, adminSupplierPath, http.StatusFound)
	return true
}
