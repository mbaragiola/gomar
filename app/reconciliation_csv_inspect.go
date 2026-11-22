package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"
)

const reconciliationUploadLimit = 11 << 20
const reconciliationFileLimit = 10 << 20

var reconciliationSingleCSV = problem{422, "Adjuntá un único archivo CSV."}

type reconciliationHeader struct {
	Index int    `json:"indice"`
	Name  string `json:"encabezado"`
}

type reconciliationInspection struct {
	Headers []reconciliationHeader `json:"columnas"`
	Records int                    `json:"registros"`
}

func readReconciliationCSVUpload(w http.ResponseWriter, r *http.Request, formLimits map[string]int64) ([]byte, map[string]string, error) {
	if r.ContentLength > reconciliationUploadLimit {
		return nil, nil, reconciliationSingleCSV
	}
	r.Body = http.MaxBytesReader(w, r.Body, reconciliationUploadLimit)
	multipart, err := r.MultipartReader()
	if err != nil {
		return nil, nil, reconciliationSingleCSV
	}
	var data []byte
	found := false
	form := map[string]string{}
	for {
		part, err := multipart.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, reconciliationSingleCSV
		}
		if part.FileName() != "" {
			if found || part.FormName() != "file" {
				return nil, nil, reconciliationSingleCSV
			}
			found = true
			data, err = io.ReadAll(io.LimitReader(part, reconciliationFileLimit+1))
			if err != nil || len(data) > reconciliationFileLimit {
				return nil, nil, reconciliationSingleCSV
			}
		} else if limit := formLimits[part.FormName()]; limit != 0 {
			value, err := io.ReadAll(io.LimitReader(part, limit+1))
			if err != nil {
				return nil, nil, reconciliationSingleCSV
			}
			form[part.FormName()] = string(value)
		}
	}
	if !found {
		return nil, nil, reconciliationSingleCSV
	}
	return data, form, nil
}

// Python's pinned strict CSV dialect preserves blank records and quoted CR/LF,
// unlike encoding/csv. Scan the supported comma/semicolon dialect without
// allocating or retaining an array of input rows.
func scanReconciliationCSV(data []byte, separator string, visit func(int, []string)) error {
	if separator != "," && separator != ";" {
		return problem{422, "El separador debe ser coma o punto y coma."}
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	if !utf8.ValidString(text) {
		return problem{422, "El CSV debe usar UTF-8."}
	}
	const (
		atStart = iota
		raw
		quoted
		afterQuote
	)
	state := atStart
	record := 0

	recordNonempty := false
	var cell strings.Builder
	fields := []string{}
	malformed := problem{422, "El CSV no tiene una estructura válida."}
	pushCell := func() error {
		if len(fields) >= 64 {
			return problem{422, fmt.Sprintf("El registro %d supera el límite de 64 columnas.", record+1)}
		}
		if utf8.RuneCountInString(cell.String()) > 1024 {
			return problem{422, fmt.Sprintf("El registro %d contiene una celda demasiado larga.", record+1)}
		}
		fields = append(fields, cell.String())
		cell.Reset()
		return nil
	}
	pushRecord := func() error {
		if recordNonempty {
			if err := pushCell(); err != nil {
				return err
			}
		}
		record++
		if record > 20001 {
			return problem{422, "El CSV supera el límite de 20.000 registros."}
		}
		if record == 1 && len(fields) == 0 {
			return problem{422, "El CSV debe incluir una fila de encabezados."}
		}
		visit(record, fields)
		fields = fields[:0]
		state = atStart
		recordNonempty = false
		return nil
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if state == quoted {
			if c == '"' {
				state = afterQuote
			} else {
				cell.WriteByte(c)
			}
			continue
		}
		if state == afterQuote && c == '"' {
			cell.WriteByte(c)
			state = quoted
			continue
		}
		switch c {
		case '\r', '\n':
			if err := pushRecord(); err != nil {
				return err
			}
			if c == '\r' && i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
		case separator[0]:
			if err := pushCell(); err != nil {
				return err
			}
			state = atStart
			recordNonempty = true
		default:
			if state == atStart && c == '"' {
				state = quoted
			} else if state == afterQuote {
				return malformed
			} else {
				cell.WriteByte(c)
				state = raw
			}
			recordNonempty = true
		}
	}
	if state == quoted {
		return malformed
	}
	if recordNonempty {
		if err := pushRecord(); err != nil {
			return err
		}
	}
	if record == 0 {
		return problem{422, "El CSV debe incluir una fila de encabezados."}
	}
	return nil
}

func inspectReconciliationCSV(data []byte, separator string) (reconciliationInspection, error) {
	info := reconciliationInspection{}
	mismatched := false
	err := scanReconciliationCSV(data, separator, func(record int, fields []string) {
		if record == 1 {
			for i, name := range fields {
				info.Headers = append(info.Headers, reconciliationHeader{i, trimDraftText(name)})
			}
		} else {
			info.Records++
			if len(fields) != len(info.Headers) {
				mismatched = true
			}
		}
	})
	if err != nil {
		return info, err
	}
	if mismatched {
		return info, problem{422, "Todos los registros deben tener la misma cantidad de columnas."}
	}
	return info, nil
}

func (a *app) reconcileInspectionScope(r *http.Request, u user, entity, id int64) error {
	return a.reconcileTransientScope(r, u, entity, id, false, 0)
}

func (a *app) reconcileTransientScope(r *http.Request, u user, entity, id int64, draftOnly bool, outlet int64) error {
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := readReconciliationCSVScope(tx, u, entity, id, outlet)
	if err != nil {
		return err
	}
	if draftOnly && run.State != "draft" {
		return problem{409, "La conciliación ya no admite cambios de archivos."}
	}
	return tx.Commit()
}

func readReconciliationCSVScope(tx *sql.Tx, u user, entity, id, outlet int64) (reconciliationReadRun, error) {
	member, err := scopedMembership(tx, u.id, entity, "reconcile_financial_data")
	if err != nil {
		return reconciliationReadRun{}, err
	}
	runs, err := scanReconciliationRuns(tx, member, id)
	if err != nil {
		return reconciliationReadRun{}, err
	}
	if len(runs) == 0 {
		return reconciliationReadRun{}, problem{404, "No se encontró la conciliación."}
	}
	run := runs[0]
	if outlet != 0 {
		allowed, err := reconciliationOutlets(tx, id)
		if err != nil {
			return run, err
		}
		if !slices.Contains(allowed, outlet) {
			return run, problem{403, "La configuración incluye un local no autorizado."}
		}
	}
	return run, nil
}

func (a *app) inspectReconciliation(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	id, err := pathID(r, "run")
	if err != nil {
		return err
	}
	if err := a.reconcileInspectionScope(r, u, entity, id); err != nil {
		return err
	}
	data, form, err := readReconciliationCSVUpload(w, r, map[string]int64{"delimiter": 17})
	if err != nil {
		return err
	}
	if err := a.reconcileInspectionScope(r, u, entity, id); err != nil {
		return err
	}
	info, err := inspectReconciliationCSV(data, csvFormValue(form, "delimiter", ","))
	if err != nil {
		return err
	}
	if err := a.reconcileInspectionScope(r, u, entity, id); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, info)
	return nil
}
