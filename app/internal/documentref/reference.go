// Package documentref defines the exact printed reference shared by the
// browser list, MCP endpoint and packaged client. It is not a unique identity.
package documentref

import (
	"net/url"
	"strconv"
)

type Reference struct {
	DocumentType string `json:"document_type" jsonschema:"invoice, credit_note o debit_note."`
	Letter       string `json:"letter" jsonschema:"Una letra mayúscula A–Z, o texto vacío si el comprobante no tiene letra."`
	PointOfSale  int64  `json:"point_of_sale" jsonschema:"Punto de venta impreso, entero entre 1 y 99999."`
	Number       int64  `json:"number" jsonschema:"Número impreso, entero entre 1 y 99999999."`
}

func (r Reference) Valid() bool {
	switch r.DocumentType {
	case "invoice", "credit_note", "debit_note":
	default:
		return false
	}
	return (r.Letter == "" || len(r.Letter) == 1 && r.Letter[0] >= 'A' && r.Letter[0] <= 'Z') &&
		r.PointOfSale >= 1 && r.PointOfSale <= 99999 && r.Number >= 1 && r.Number <= 99999999
}

func (r Reference) AddQuery(q url.Values) {
	q.Set("document_type", r.DocumentType)
	q.Set("letter", r.Letter)
	q.Set("point_of_sale", strconv.FormatInt(r.PointOfSale, 10))
	q.Set("number", strconv.FormatInt(r.Number, 10))
}

// TakeQuery recognizes an optional all-or-none exact reference and consumes
// those fields. The caller remains responsible for other query parameters.
func TakeQuery(q url.Values) (*Reference, bool) {
	keys := [...]string{"document_type", "letter", "point_of_sale", "number"}
	var present bool
	for _, key := range keys {
		if _, exists := q[key]; exists {
			present = true
		}
	}
	if !present {
		return nil, true
	}
	for _, key := range keys {
		if len(q[key]) != 1 {
			return nil, false
		}
	}
	point, pointErr := strconv.ParseInt(q.Get("point_of_sale"), 10, 64)
	number, numberErr := strconv.ParseInt(q.Get("number"), 10, 64)
	r := Reference{q.Get("document_type"), q.Get("letter"), point, number}
	if pointErr != nil || numberErr != nil || !r.Valid() ||
		q.Get("point_of_sale") != strconv.FormatInt(point, 10) || q.Get("number") != strconv.FormatInt(number, 10) {
		return nil, false
	}
	for _, key := range keys {
		q.Del(key)
	}
	return &r, true
}

const DraftStatus = "draft"

// TakeDraftStatus recognizes the only supported status filter. An explicit
// empty, other or repeated value is not an unfiltered request.
func TakeDraftStatus(q url.Values) (draftOnly, valid bool) {
	values, present := q["status"]
	if !present {
		return false, true
	}
	if len(values) != 1 || values[0] != DraftStatus {
		return false, false
	}
	q.Del("status")
	return true, true
}
