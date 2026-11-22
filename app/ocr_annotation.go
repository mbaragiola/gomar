package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errOCRAnnotation = errors.New("El proveedor devolvió una respuesta que no se pudo validar. Podés completar los datos manualmente.")

var ocrTextFields = [...]string{
	"supplier_legal_name", "issuer_cuit", "recipient_cuit", "document_type", "letter", "issue_date",
	"due_date", "currency", "subtotal", "tax", "total",
}

func boundedOCRText(v any) bool {
	s, ok := v.(string)
	return ok && utf8.RuneCountInString(s) <= 200
}

func boundedOCRInteger(v any, max int64) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return err == nil && i >= 1 && i <= max
}

// parseOCRAnnotation handles only local JSON results, not any wire/provider envelope.
func parseOCRAnnotation(raw []byte) (map[string]any, error) {
	var obj map[string]any
	if len(raw) > 1<<20 || !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, errOCRAnnotation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&obj) != nil || len(obj) != len(ocrTextFields)+3 {
		return nil, errOCRAnnotation
	}
	for _, key := range ocrTextFields {
		v, present := obj[key]
		if !present || v != nil && !boundedOCRText(v) {
			return nil, errOCRAnnotation
		}
	}
	for key, max := range map[string]int64{"point_of_sale": 99999, "number": 99999999} {
		v, present := obj[key]
		if !present || v != nil && !boundedOCRInteger(v, max) {
			return nil, errOCRAnnotation
		}
	}
	v, present := obj["tax_breakdown"]
	if !present {
		return nil, errOCRAnnotation
	}
	if v != nil {
		entries, ok := v.([]any)
		if !ok || len(entries) > 20 {
			return nil, errOCRAnnotation
		}
		for _, entry := range entries {
			item, ok := entry.(map[string]any)
			if !ok || len(item) != 2 || !boundedOCRText(item["label"]) || !boundedOCRText(item["amount"]) {
				return nil, errOCRAnnotation
			}
		}
	}
	return obj, nil
}

type ocrSupplier struct {
	ID     int64  `json:"id"`
	Name   string `json:"razon_social"`
	CUIT   string `json:"cuit"`
	Active bool   `json:"-"`
}

// ocrSuggestions produces editable fields only. Its call site would be responsible
// for supplying same-entity lookup results; this package adds no handler or lookup.
func ocrSuggestions(raw []byte, entityCUIT string, lookup func(string) *ocrSupplier) (map[string]any, error) {
	annotation, err := parseOCRAnnotation(raw)
	if err != nil {
		return nil, err
	}
	text := func(key string) string {
		s, _ := annotation[key].(string)
		return s
	}
	warnings := []string{"Los datos son sugerencias editables. El OCR no prueba identidad, autenticidad ni validez fiscal."}
	normalized := make(map[string]any, 2)
	for _, field := range []struct{ key, label string }{{"issuer_cuit", "emisor"}, {"recipient_cuit", "receptor"}} {
		if annotation[field.key] == nil {
			normalized[field.key] = nil
			if field.label == "emisor" {
				warnings = append(warnings, "No se pudo identificar el CUIT emisor; tenés que completarlo.")
			}
			continue
		}
		raw := text(field.key)
		cuit, err := normalizeCUIT(trimOCRText(raw))
		if err != nil {
			normalized[field.key] = raw
			warnings = append(warnings, "El CUIT del "+field.label+" no superó la validación y requiere corrección.")
		} else {
			normalized[field.key] = cuit
		}
	}
	issuer, _ := normalized["issuer_cuit"].(string)
	recipient, _ := normalized["recipient_cuit"].(string)
	if issuer != "" && recipient != "" && issuer == recipient {
		warnings = append(warnings, "Emisor y receptor tienen el mismo CUIT; confirmá los roles antes de guardar.")
	}
	if recipient != "" && recipient != entityCUIT {
		warnings = append(warnings, "El CUIT receptor no coincide con la entidad seleccionada.")
	}
	var match *ocrSupplier
	state := "sin_coincidencia"
	if lookup != nil && len(issuer) == 11 && strings.Trim(issuer, "0123456789") == "" {
		if candidate := lookup(issuer); candidate != nil {
			if candidate.Active {
				state, match = "coincidencia", candidate
			} else {
				state = "inactivo"
				warnings = append(warnings, "El CUIT coincide con un proveedor inactivo; no se puede asignar ni crear automáticamente.")
			}
		}
	}
	docType := map[string]string{
		"invoice": "invoice", "factura": "invoice", "credit_note": "credit_note", "nota de crédito": "credit_note",
		"debit_note": "debit_note", "nota de débito": "debit_note",
	}[strings.ToLower(trimOCRText(text("document_type")))]
	if text("document_type") != "" && docType == "" {
		warnings = append(warnings, "El tipo de comprobante extraído no es compatible; elegilo manualmente.")
	}
	currency := strings.ToUpper(trimOCRText(text("currency")))
	if currency != "" && currency != "ARS" && currency != "USD" {
		warnings = append(warnings, "La moneda extraída no es compatible; elegila manualmente.")
		currency = ""
	}
	letter := trimOCRText(text("letter"))
	// Validate the uppercase value as ASCII; Python upper() also
	// maps the non-ASCII dotless i and long s to single ASCII letters.
	letter = strings.ToUpper(letter)
	if letter != "" && (len(letter) != 1 || letter[0] < 'A' || letter[0] > 'Z') {
		warnings = append(warnings, "La letra extraída es ambigua o no válida; completala manualmente.")
		letter = ""
	}
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	return map[string]any{
		"razon_social_proveedor": annotation["supplier_legal_name"],
		"cuit_emisor":            normalized["issuer_cuit"], "cuit_receptor": normalized["recipient_cuit"],
		"tipo": nullable(docType), "letra": letter, "punto_venta": annotation["point_of_sale"], "numero": annotation["number"],
		"fecha_emision": annotation["issue_date"], "fecha_vencimiento": annotation["due_date"], "moneda": nullable(currency),
		"neto": annotation["subtotal"], "iva": annotation["tax"], "total": annotation["total"],
		"desglose_impuestos": annotation["tax_breakdown"], "proveedor": map[string]any{"estado": state, "coincidencia": match},
		"advertencias": warnings,
	}, nil
}

func trimOCRText(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f })
}
