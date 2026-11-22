package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

var reconciliationCSVFormLimits = map[string]int64{
	"delimiter": 17, "date_format": 128, "decimal_separator": 17, "mapping": 4096, "outlet_mapping": 4096,
	"default_outlet_id": 128, "default_currency": 64,
}

type reconciliationPreviewConfig struct {
	mapping                              map[string]json.RawMessage
	date, separator, delimiter, currency string
	outlet                               int64
	outlets                              map[string]int64
}

type reconciliationPreviewRow struct {
	Record         int     `json:"record_number"`
	Outlet         int64   `json:"outlet_id"`
	Date           string  `json:"date"`
	Amount         string  `json:"amount"`
	Currency       string  `json:"currency"`
	CurrencySource string  `json:"currency_source"`
	Category       string  `json:"category"`
	Description    string  `json:"description"`
	Method         string  `json:"payment_method"`
	Reference      string  `json:"reference"`
	VATRate        *string `json:"vat_rate"`
	VATAmount      *string `json:"vat_amount"`
}

func csvFormValue(form map[string]string, key, fallback string) string {
	if value, exists := form[key]; exists {
		return value
	}
	return fallback
}

func decodeReconciliationPreview(form map[string]string) (reconciliationPreviewConfig, error) {
	config := reconciliationPreviewConfig{
		date: form["date_format"], currency: form["default_currency"],
		delimiter: csvFormValue(form, "delimiter", ","), separator: csvFormValue(form, "decimal_separator", "."),
	}
	mapping := csvFormValue(form, "mapping", "{}")
	outlets := csvFormValue(form, "outlet_mapping", "{}")
	if len(mapping) > 4096 || len(outlets) > 4096 || len(form["default_outlet_id"]) > 128 || !json.Valid([]byte(mapping)) || !json.Valid([]byte(outlets)) {
		return config, problem{422, "La configuración del CSV no es válida."}
	}
	_ = json.Unmarshal([]byte(mapping), &config.mapping)
	if _, mapped := config.mapping["currency"]; mapped {
		config.currency = ""
	}
	var outletMapping map[string]*int64
	if err := json.Unmarshal([]byte(outlets), &outletMapping); err != nil || outletMapping == nil {
		return config, problem{422, "La resolución de locales no es válida."}
	}
	config.outlets = map[string]int64{}
	for label, value := range outletMapping {
		if value == nil {
			return config, problem{422, "La resolución de locales no es válida."}
		}
		config.outlets[label] = *value
	}
	if outlet := form["default_outlet_id"]; outlet != "" {
		var err error
		config.outlet, err = strconv.ParseInt(strings.TrimSpace(outlet), 10, 64)
		if err != nil {
			return config, problem{422, "La configuración del CSV no es válida."}
		}
	}
	return config, nil
}

func previewMapping(config reconciliationPreviewConfig, width int) (map[string]int, error) {
	if _, ok := config.mapping["date"]; !ok {
		return nil, problem{422, "La fecha y el importe requieren un mapeo explícito."}
	}
	if _, ok := config.mapping["amount"]; !ok {
		return nil, problem{422, "La fecha y el importe requieren un mapeo explícito."}
	}
	result := make(map[string]int, len(config.mapping))
	used := map[int]bool{}
	for field, raw := range config.mapping {
		switch field {
		case "date", "amount", "category", "description", "payment_method", "reference", "currency", "outlet", "vat_amount", "vat_rate":
		default:
			return nil, problem{422, "El mapeo contiene un campo no admitido."}
		}
		index, err := strconv.Atoi(string(raw))
		if err != nil || index < 0 || index >= width {
			return nil, problem{422, fmt.Sprintf("El índice del campo %s no es válido.", field)}
		}
		if used[index] {
			return nil, problem{422, "Una columna no puede mapearse a más de un campo."}
		}
		result[field] = index
		used[index] = true
	}
	return result, nil
}

func csvFieldProblem(record int, field, reason string) error {
	return problem{422, fmt.Sprintf("Registro %d, campo %s: %s.", record, field, reason)}
}

func previewCSVAmount(value, separator, field string, record int) (string, error) {
	if separator != "." && separator != "," {
		return "", problem{422, "El separador decimal no es válido."}
	}
	negative := strings.HasPrefix(value, "-")
	if strings.HasPrefix(value, "+") || negative {
		value = value[1:]
	}
	parts := strings.Split(value, separator)
	invalid := csvFieldProblem(record, field, "el importe no tiene un formato válido")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return "", invalid
	}
	normalized := make([]string, len(parts))
	for i, part := range parts {
		var digits strings.Builder
		for _, r := range part {
			r = decimalDraftRune(r)
			if r < '0' || r > '9' {
				return "", invalid
			}
			digits.WriteRune(r)
		}
		normalized[i] = digits.String()
	}
	whole := strings.TrimLeft(normalized[0], "0")
	if len(whole) > 12 {
		return "", csvFieldProblem(record, field, "el importe está fuera del rango permitido")
	}
	if whole == "" {
		whole = "0"
	}
	fraction := "00"
	if len(normalized) == 2 {
		fraction = normalized[1]
		if len(fraction) == 0 || len(fraction) > 2 {
			return "", invalid
		}
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	if negative && (whole != "0" || fraction != "00") {
		whole = "-" + whole
	}
	return whole + "." + fraction, nil
}

// The pinned preview preserves input decimal scale; the stored rate uses
// the DecimalField's fixed four-place representation.
func previewCSVVATRate(value, separator string, record int) (*string, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, separator)
	bad := csvFieldProblem(record, "vat_rate", "la alícuota no tiene un formato válido")
	if len(parts) > 2 || parts[0] == "" {
		return nil, bad
	}
	normalized := make([]string, len(parts))
	for i, part := range parts {
		var digits strings.Builder
		if part == "" {
			return nil, bad
		}
		for _, r := range part {
			r = decimalDraftRune(r)
			if r < '0' || r > '9' {
				return nil, bad
			}
			digits.WriteRune(r)
		}
		normalized[i] = digits.String()
	}
	whole := strings.TrimLeft(normalized[0], "0")
	if whole == "" {
		whole = "0"
	}
	fraction := ""
	if len(normalized) == 2 {
		fraction = normalized[1]
		if len(fraction) > 4 {
			return nil, bad
		}
	}
	if len(whole) > 3 || (len(whole) == 3 && (whole > "100" || strings.Trim(fraction, "0") != "")) {
		return nil, csvFieldProblem(record, "vat_rate", "la alícuota está fuera del rango permitido")
	}
	if fraction != "" {
		whole += "." + fraction
	}
	return &whole, nil
}

func storedCSVVATRate(rate *string) *string {
	if rate == nil {
		return nil
	}
	whole, fraction, _ := strings.Cut(*rate, ".")
	normalized := whole + "." + fraction + strings.Repeat("0", 4-len(fraction))
	return &normalized
}

func previewReconciliationCSV(data []byte, config reconciliationPreviewConfig) ([]reconciliationPreviewRow, error) {
	return parseReconciliationFixedCSV(data, config, 20)
}

func parseReconciliationFixedCSV(data []byte, config reconciliationPreviewConfig, keep int) ([]reconciliationPreviewRow, error) {
	result := []reconciliationPreviewRow{}
	layout := map[string]string{"%Y-%m-%d": "2006-1-2", "%d/%m/%Y": "2/1/2006", "%d-%m-%Y": "2-1-2006"}[config.date]
	var validation error
	var mapping map[string]int
	var width int
	err := scanReconciliationCSV(data, config.delimiter, func(record int, fields []string) {
		if validation != nil {
			return
		}
		if record == 1 {
			width = len(fields)
			mapping, validation = previewMapping(config, width)
			if validation != nil {
				return
			}
			_, mappedCurrency := mapping["currency"]
			_, mappedOutlet := mapping["outlet"]
			if layout == "" {
				validation = problem{422, "El formato de fecha no es válido."}
			} else if !mappedCurrency && config.currency != "ARS" && config.currency != "USD" {
				validation = problem{422, "Elegí una moneda válida para el archivo."}
			} else if !mappedOutlet && config.outlet == 0 {
				validation = problem{422, "Elegí un local para el archivo."}
			}
			return
		}
		if len(fields) != width {
			validation = problem{422, fmt.Sprintf("El registro %d no tiene la cantidad de columnas esperada.", record)}
			return
		}
		read := func(key string) string {
			if idx, ok := mapping[key]; ok {
				return trimDraftText(fields[idx])
			}
			return ""
		}
		date, err := time.Parse(layout, read("date"))
		if err != nil || date.Year() < 1 {
			validation = csvFieldProblem(record, "date", "la fecha no tiene el formato elegido")
			return
		}
		amount, err := previewCSVAmount(read("amount"), config.separator, "amount", record)
		if err != nil {
			validation = err
			return
		}
		currency, currencySource := config.currency, "supplied"
		if _, mapped := mapping["currency"]; mapped {
			currency, currencySource = read("currency"), "imported"
			if currency != "ARS" && currency != "USD" {
				validation = csvFieldProblem(record, "currency", "la moneda no es válida")
				return
			}
		}
		outlet := config.outlet
		if _, mapped := mapping["outlet"]; mapped {
			resolved := read("outlet")
			var ok bool
			outlet, ok = config.outlets[resolved]
			if resolved == "" || !ok {
				validation = csvFieldProblem(record, "outlet", "el local no fue resuelto")
				return
			}
		}
		var vatAmount *string
		if raw := read("vat_amount"); raw != "" {
			normalized, err := previewCSVAmount(raw, config.separator, "vat_amount", record)
			if err != nil {
				validation = err
				return
			}
			vatAmount = &normalized
		}
		vatRate, err := previewCSVVATRate(read("vat_rate"), config.separator, record)
		if err != nil {
			validation = err
			return
		}
		if len(result) < keep {
			result = append(result, reconciliationPreviewRow{Record: record, Outlet: outlet, Date: date.Format(time.DateOnly),
				Amount: amount, Currency: currency, CurrencySource: currencySource, Category: read("category"),
				Description: read("description"), Method: read("payment_method"), Reference: read("reference"), VATRate: vatRate, VATAmount: vatAmount})
		}
	})
	if err != nil {
		return nil, err
	}
	return result, validation
}

func reconciliationOutletLabels(data []byte, config reconciliationPreviewConfig) ([]string, error) {
	labels := []string{}
	seen := map[string]bool{}
	var validation error
	var width int
	index := -1
	err := scanReconciliationCSV(data, config.delimiter, func(record int, fields []string) {
		if validation != nil {
			return
		}
		if record == 1 {
			width = len(fields)
			mapping, err := previewMapping(config, width)
			if err != nil {
				validation = err
				return
			}
			if found, ok := mapping["outlet"]; ok {
				index = found
			}
			return
		}
		if index < 0 || len(fields) != width {
			return
		}
		label := trimDraftText(fields[index])
		if label != "" && !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(labels)
	return labels, validation
}

func validateReconciliationCSVOutlets(tx *sql.Tx, id int64, config reconciliationPreviewConfig) error {
	allowed, err := reconciliationOutlets(tx, id)
	if err != nil {
		return err
	}
	for _, outlet := range config.outlets {
		if !slices.Contains(allowed, outlet) {
			return problem{403, "La configuración incluye un local no autorizado."}
		}
	}
	return nil
}

func (a *app) reconcileCSVConfigScope(r *http.Request, u user, entity, id int64, draftOnly bool, config reconciliationPreviewConfig) error {
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	run, err := readReconciliationCSVScope(tx, u, entity, id, config.outlet)
	if err != nil {
		return err
	}
	if draftOnly && run.State != "draft" {
		return problem{409, "La conciliación ya no admite cambios de archivos."}
	}
	if err := validateReconciliationCSVOutlets(tx, id, config); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *app) previewReconciliation(w http.ResponseWriter, r *http.Request, u user) error {
	entity, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	id, err := pathID(r, "run")
	if err != nil {
		return err
	}
	if err := a.reconcileTransientScope(r, u, entity, id, true, 0); err != nil {
		return err
	}
	data, form, err := readReconciliationCSVUpload(w, r, reconciliationCSVFormLimits)
	if err != nil {
		return err
	}
	config, err := decodeReconciliationPreview(form)
	if err != nil {
		return err
	}
	if err := a.reconcileCSVConfigScope(r, u, entity, id, true, config); err != nil {
		return err
	}
	labels := []string{}
	if _, mapped := config.mapping["outlet"]; mapped {
		labels, err = reconciliationOutletLabels(data, config)
		if err != nil {
			return err
		}
		for _, label := range labels {
			if _, ok := config.outlets[label]; !ok {
				if err := a.reconcileCSVConfigScope(r, u, entity, id, true, config); err != nil {
					return err
				}
				jsonResponse(w, http.StatusOK, map[string]any{"rows": []any{}, "outlet_labels": labels, "needs_outlet_mapping": true})
				return nil
			}
		}
	}
	rows, err := previewReconciliationCSV(data, config)
	if err != nil {
		return err
	}
	if err := a.reconcileCSVConfigScope(r, u, entity, id, true, config); err != nil {
		return err
	}
	jsonResponse(w, http.StatusOK, map[string]any{"rows": rows, "outlet_labels": labels, "truncated": len(rows) == 20})
	return nil
}
