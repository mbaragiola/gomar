package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (a *app) createSupplier(w http.ResponseWriter, r *http.Request, u user) error {
	return a.saveSupplier(w, r, u, false)
}

func (a *app) updateSupplier(w http.ResponseWriter, r *http.Request, u user) error {
	return a.saveSupplier(w, r, u, true)
}

func (a *app) saveSupplier(w http.ResponseWriter, r *http.Request, u user, updating bool) error {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	var id int64
	if updating {
		id, err = pathID(r, "supplier")
		if err != nil {
			return err
		}
	}
	var input struct {
		Name *string `json:"razon_social"`
		CUIT *string `json:"cuit"`
	}
	// Like Ninja, decode the required request fields before running the handler.
	// Read the body before taking a transaction so the client cannot hold it open.
	if err := parseJSON(w, r, &input); err != nil {
		return err
	}
	if input.Name == nil || input.CUIT == nil {
		return badInput
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entityID, "manage_suppliers")
	if err != nil {
		return err
	}
	if err := lockPostgresEntity(r.Context(), tx, entityID); errors.Is(err, sql.ErrNoRows) {
		return missingEntity
	} else if err != nil {
		return err
	}
	member, err = scopedMembership(tx, u.id, entityID, "manage_suppliers")
	if err != nil {
		return err
	}
	if err := requireSupplierOutlet(tx, member); err != nil {
		return err
	}
	var existingCUIT string
	var hasPayable bool
	if updating {
		err := tx.QueryRow(`SELECT s.cuit, EXISTS(SELECT 1 FROM ap_payables p WHERE p.supplier_id=s.id)
			FROM suppliers s WHERE s.id=$1 AND s.entity_id=$2`, id, member.ID).Scan(&existingCUIT, &hasPayable)
		if errors.Is(err, sql.ErrNoRows) {
			return problem{404, "No se encontró el proveedor."}
		}
		if err != nil {
			return err
		}
	}
	name := strings.TrimSpace(*input.Name)
	if name == "" {
		return problem{422, "La razón social es obligatoria."}
	}
	cuit, err := normalizeCUIT(*input.CUIT)
	if err != nil {
		return err
	}
	status := http.StatusCreated
	if updating {
		// A name change is allowed; the payable retains its original supplier snapshot.
		if hasPayable && cuit != existingCUIT {
			return problem{409, "No se puede cambiar el CUIT de un proveedor con historial en cuentas a pagar."}
		}
		status = http.StatusOK
		_, err = tx.Exec("UPDATE suppliers SET name=$1,cuit=$2 WHERE id=$3 AND entity_id=$4", name, cuit, id, member.ID)
	} else {
		id, err = insertSupplierWithCreation(r.Context(), tx, member.ID, name, cuit, true, time.Now())
	}
	if err != nil {
		if isUnique(err) {
			return problem{409, "Ya existe un proveedor con ese CUIT en la entidad."}
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, status, supplier{ID: id, Name: name, CUIT: cuit})
	return nil
}

// Supplier creation is written once, with the UTC microsecond precision of
// Django's catalog timestamp. Unknown dates of already-stored rows stay NULL.
const microsecondsUTC = "2006-01-02T15:04:05.000000Z"

func insertSupplierWithCreation(ctx context.Context, tx *sql.Tx, entityID int64, name, cuit string, active bool, now time.Time) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, "INSERT INTO suppliers(entity_id,name,cuit,active,created_at) VALUES($1,$2,$3,$4,$5) RETURNING id",
		entityID, name, cuit, active, now.UTC().Format(microsecondsUTC)).Scan(&id)
	return id, err
}
