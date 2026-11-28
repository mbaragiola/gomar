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

const adminOutletAddPath = adminOutletPath + "add/"
const adminSupplierAddPath = adminSupplierPath + "add/"

type adminOutletEntityOption struct {
	ID   int64
	Name string
}

type adminCatalogAddPage struct {
	Name, Problem, CUIT, FormID, FormPath, NameLabel, NameField string
	EntityID, MembershipID                                      int64
	Active, Supplier, Legal                                     bool
	Entities, InitialGrantees                                   []adminOutletEntityOption
	MaxName                                                     int
}

func (a *app) serveAdminCatalogAdd(w http.ResponseWriter, r *http.Request, page adminPage, actorID int64, supplier bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fields := adminCatalogAddPage{Active: true, FormID: "outlet_form", FormPath: adminOutletAddPath,
		NameLabel: "Nombre", NameField: "name", MaxName: 120, Supplier: supplier}
	page.Title = "Añadir local"
	if supplier {
		fields.FormID, fields.FormPath, fields.NameLabel, fields.NameField, fields.MaxName =
			"supplier_form", adminSupplierAddPath, "Razón social", "business_name", 160
		page.Title = "Agregar proveedor"
	}
	if r.Method == http.MethodPost && a.saveAdminCatalogAdd(w, r, actorID, &fields) {
		return
	}
	rows, err := a.db.QueryContext(r.Context(), "SELECT id,name FROM entities ORDER BY name,id")
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var entity adminOutletEntityOption
		if err := rows.Scan(&entity.ID, &entity.Name); err != nil {
			failure(w, err)
			return
		}
		fields.Entities = append(fields.Entities, entity)
	}
	if err := rows.Err(); err != nil {
		failure(w, err)
		return
	}
	if !supplier {
		eligible, err := a.db.QueryContext(r.Context(), "SELECT m.id,e.name || ' · ' || u.username"+initialOutletGrantee+" ORDER BY e.name,u.username,m.id")
		if err != nil {
			failure(w, err)
			return
		}
		defer eligible.Close()
		for eligible.Next() {
			var option adminOutletEntityOption
			if err := eligible.Scan(&option.ID, &option.Name); err != nil {
				failure(w, err)
				return
			}
			fields.InitialGrantees = append(fields.InitialGrantees, option)
		}
		if err := eligible.Err(); err != nil {
			failure(w, err)
			return
		}
	}
	page.CatalogAdd = &fields
	renderAdmin(w, page)
}

func (a *app) saveAdminCatalogAdd(w http.ResponseWriter, r *http.Request, actorID int64, fields *adminCatalogAddPage) bool {
	for key, values := range r.PostForm {
		switch key {
		case fields.NameField, "csrfmiddlewaretoken", "entity", "active", "_save":
			if len(values) == 1 {
				continue
			}
		case "initial_membership":
			if !fields.Supplier && len(values) == 1 {
				continue
			}
		case "cuit":
			if fields.Supplier && len(values) == 1 {
				continue
			}
		}
		http.Error(w, "Formulario de local no compatible.", http.StatusBadRequest)
		return true
	}
	fields.Name = strings.TrimSpace(r.PostForm.Get(fields.NameField))
	fields.Active = r.PostForm.Get("active") == "on"
	id, err := strconv.ParseInt(r.PostForm.Get("entity"), 10, 64)
	fields.EntityID = id
	switch {
	case err != nil || id < 1:
		fields.Problem = "Elegí una entidad válida."
		return false
	case fields.Name == "":
		fields.Problem = "Este campo es obligatorio."
		return false
	case !utf8.ValidString(fields.Name) || utf8.RuneCountInString(fields.Name) > fields.MaxName:
		fields.Problem = "El nombre no puede superar " + strconv.Itoa(fields.MaxName) + " caracteres."
		return false
	case r.PostForm.Has("active") && !fields.Active:
		http.Error(w, "Estado de local no compatible.", http.StatusBadRequest)
		return true
	}
	var cuit string
	if fields.Supplier {
		fields.CUIT = strings.TrimSpace(r.PostForm.Get("cuit"))
		cuit, err = normalizeCUIT(fields.CUIT)
		if err != nil || utf8.RuneCountInString(fields.CUIT) > 14 {
			fields.Problem = "Ingresá un CUIT válido."
			return false
		}
	}
	if !fields.Supplier {
		fields.MembershipID, err = strconv.ParseInt(r.PostForm.Get("initial_membership"), 10, 64)
		if err != nil || fields.MembershipID < 1 {
			fields.Problem = invalidInitialOutletGrantee.detail
			return false
		}
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		failure(w, err)
		return true
	}
	defer tx.Rollback()
	err = lockPostgresEntity(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		fields.Problem = "Elegí una entidad válida."
		return false
	}
	if err != nil {
		failure(w, err)
		return true
	}
	var recordID int64
	if fields.Supplier {
		recordID, err = insertSupplierWithCreation(ctx, tx, id, fields.Name, cuit, fields.Active, time.Now())
	} else {
		err = tx.QueryRowContext(ctx, "INSERT INTO outlets(entity_id,name,active) VALUES($1,$2,$3) RETURNING id", id, fields.Name, fields.Active).Scan(&recordID)
	}
	if isUnique(err) {
		fields.Problem = "Ya existe un local con ese nombre en la entidad."
		if fields.Supplier {
			fields.Problem = "Ya existe un proveedor con ese CUIT en la entidad."
		}
		return false
	}
	if err != nil {
		failure(w, err)
		return true
	}
	if !fields.Supplier {
		if err := grantNewOutlet(ctx, tx, id, recordID, fields.MembershipID, actorID); err != nil {
			if err == invalidInitialOutletGrantee {
				fields.Problem = invalidInitialOutletGrantee.detail
				return false
			}
			failure(w, err)
			return true
		}
	}
	list := adminOutletPath
	if err == nil {
		at := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
		if fields.Supplier {
			list = adminSupplierPath
			_, err = tx.ExecContext(ctx, `INSERT INTO admin_supplier_history(supplier_id,actor_id,occurred_at,name_changed,action)
				VALUES($1,$2,$3,FALSE,'add')`, recordID, actorID, at)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO admin_outlet_history(outlet_id,actor_id,occurred_at,name_changed,active_changed,action)
				VALUES($1,$2,$3,FALSE,FALSE,'add')`, recordID, actorID, at)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		failure(w, err)
		return true
	}
	http.Redirect(w, r, list, http.StatusFound)
	return true
}
