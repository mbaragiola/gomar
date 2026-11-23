package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const draftCapability = "manage_draft_documents"
const draftCreateReason = "Alta de comprobante"

var duplicateDocument = problem{409, "Ya existe ese comprobante para el CUIT emisor en la entidad."}

type draftInput struct {
	Outlet      *int64  `json:"outlet_id"`
	Supplier    *int64  `json:"supplier_id"`
	Issuer      *string `json:"issuer_cuit"`
	Kind        *string `json:"tipo"`
	Letter      string  `json:"letra"`
	Point       *int64  `json:"punto_venta"`
	Number      *int64  `json:"numero"`
	Issue       *string `json:"fecha_emision"`
	Due         *string `json:"fecha_vencimiento"`
	Currency    *string `json:"moneda"`
	Net         *string `json:"neto"`
	Tax         *string `json:"iva"`
	Total       *string `json:"total"`
	Description string  `json:"descripcion"`
}

func decodeDraft(body []byte) (draftInput, error) {
	return decodeDraftFields(body, true)
}

func decodeDraftFields(body []byte, requireSupplier bool) (draftInput, error) {
	var data draftInput
	if err := decodeJSON(body, &data); err != nil {
		return data, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return data, badInput
	}
	if data.Outlet == nil || requireSupplier && data.Supplier == nil || data.Issuer == nil || data.Kind == nil ||
		data.Point == nil || data.Number == nil || data.Issue == nil || data.Currency == nil ||
		data.Total == nil || string(fields["letra"]) == "null" || string(fields["descripcion"]) == "null" {
		return data, badInput
	}
	for _, date := range []*string{data.Issue, data.Due} {
		if date != nil {
			parsed, err := time.Parse(time.DateOnly, *date)
			if err != nil || parsed.Year() < 1 {
				return data, badInput
			}
		}
	}
	return data, nil
}

func trimDraftText(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
	})
}

var decimalDraft = regexp.MustCompile(`^([+-]?)([0-9]*)(?:\.([0-9]*))?(?:[eE]([+-]?[0-9]+))?$`)

func decimalDraftRune(r rune) rune {
	if r == '_' {
		return -1
	}
	if r <= unicode.MaxASCII {
		return r
	}
	// Decimal accepts decimal Unicode digits in addition to ASCII digits.
	for _, group := range unicode.Digit.R16 {
		code := uint32(r)
		lo, hi, step := uint32(group.Lo), uint32(group.Hi), uint32(group.Stride)
		if code >= lo && code <= hi && (code-lo)%step == 0 {
			return '0' + rune((code-lo)/step%10)
		}
	}
	for _, group := range unicode.Digit.R32 {
		code := uint32(r)
		if code >= group.Lo && code <= group.Hi && (code-group.Lo)%group.Stride == 0 {
			return '0' + rune((code-group.Lo)/group.Stride%10)
		}
	}
	return r
}

func draftMoney(raw *string, label string, required bool) (*int64, error) {
	invalid := problem{422, label + ": ingresá un importe válido."}
	if raw == nil || trimDraftText(*raw) == "" {
		if required {
			return nil, invalid
		}
		return nil, nil
	}
	value := trimDraftText(*raw)
	if strings.Count(value, ".")+strings.Count(value, ",") > 1 {
		return nil, invalid
	}
	value = strings.Map(decimalDraftRune, strings.ReplaceAll(value, ",", "."))
	parts := decimalDraft.FindStringSubmatch(value)
	if parts == nil || parts[2]+parts[3] == "" {
		return nil, invalid
	}
	var exponent int64
	if parts[4] != "" {
		var err error
		exponent, err = strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			return nil, invalid
		}
	}
	fractionLength := int64(len(parts[3]))
	if exponent < fractionLength-2 {
		return nil, invalid
	}
	digits := strings.TrimLeft(parts[2]+parts[3], "0")
	if digits == "" {
		var zero int64
		return &zero, nil
	}
	// The oracle caps amounts at 999,999,999,999.99. Bound the exponent
	// before padding and preserve the decimal representation's precision.
	if parts[1] == "-" || exponent > fractionLength+12-int64(len(digits)) {
		return nil, invalid
	}
	minor, err := strconv.ParseInt(digits+strings.Repeat("0", int(exponent-fractionLength+2)), 10, 64)
	if err != nil {
		return nil, invalid
	}
	return &minor, nil
}

func (a *app) createDraft(w http.ResponseWriter, r *http.Request, u user) error {
	entityID, err := pathID(r, "entity")
	if err != nil {
		return err
	}
	body, err := readJSONBody(w, r)
	if err != nil {
		return err
	}
	data, err := decodeDraft(body)
	if err != nil {
		return err
	}
	tx, err := a.beginEmployeeCheckedWrite(r, u)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	member, err := scopedMembership(tx, u.id, entityID, draftCapability)
	if err != nil {
		return err
	}
	if err := a.lockDocumentWrite(r, tx, u, entityID); err != nil {
		return err
	}
	member, err = scopedMembership(tx, u.id, entityID, draftCapability)
	if err != nil {
		return err
	}
	item, err := persistDraft(tx, member, data)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	jsonResponse(w, http.StatusCreated, item)
	return nil
}

type validatedDraftValues struct {
	CUIT, Letter    string
	Net, Tax, Total *int64
}

func validateDraft(tx *sql.Tx, member entityMembership, data draftInput) (validatedDraftValues, error) {
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM outlets o JOIN outlet_grants g ON g.outlet_id=o.id
		WHERE o.id=$1 AND o.entity_id=$2 AND o.active AND g.membership_id=$3)`,
		*data.Outlet, member.ID, member.id).Scan(&exists); err != nil {
		return validatedDraftValues{}, err
	}
	if !exists {
		return validatedDraftValues{}, missingOutlet
	}
	var supplierCUIT string
	err := tx.QueryRow("SELECT cuit FROM suppliers WHERE id=$1 AND entity_id=$2 AND active",
		*data.Supplier, member.ID).Scan(&supplierCUIT)
	if errors.Is(err, sql.ErrNoRows) {
		return validatedDraftValues{}, problem{404, "No se encontró el proveedor."}
	}
	if err != nil {
		return validatedDraftValues{}, err
	}
	cuit, err := normalizeCUIT(*data.Issuer)
	if err != nil {
		return validatedDraftValues{}, err
	}
	if cuit != supplierCUIT {
		return validatedDraftValues{}, problem{422, "Confirmá el CUIT emisor: debe coincidir con el proveedor elegido."}
	}
	if _, valid := documentTypes[*data.Kind]; !valid {
		return validatedDraftValues{}, problem{422, "El tipo de comprobante no es válido."}
	}
	letter := strings.ToUpper(trimDraftText(data.Letter))
	if letter != "" && (len(letter) != 1 || letter[0] < 'A' || letter[0] > 'Z') {
		return validatedDraftValues{}, problem{422, "La letra debe ser una letra de A a Z o quedar vacía."}
	}
	if *data.Point < 1 || *data.Point > 99999 {
		return validatedDraftValues{}, problem{422, "El punto de venta debe estar entre 1 y 99999."}
	}
	if *data.Number < 1 || *data.Number > 99999999 {
		return validatedDraftValues{}, problem{422, "El número debe estar entre 1 y 99999999."}
	}
	if *data.Currency != "ARS" && *data.Currency != "USD" {
		return validatedDraftValues{}, problem{422, "La moneda no es válida."}
	}
	net, err := draftMoney(data.Net, "Neto", false)
	if err != nil {
		return validatedDraftValues{}, err
	}
	tax, err := draftMoney(data.Tax, "IVA", false)
	if err != nil {
		return validatedDraftValues{}, err
	}
	total, err := draftMoney(data.Total, "Total", true)
	if err != nil {
		return validatedDraftValues{}, err
	}
	return validatedDraftValues{cuit, letter, net, tax, total}, nil
}

func persistDraft(tx *sql.Tx, member entityMembership, data draftInput) (documentView, error) {
	id, err := insertDraft(tx, member, data, draftCreateReason)
	if err != nil {
		return documentView{}, err
	}
	return readDocumentView(tx, member, id)
}

func insertDraft(tx *sql.Tx, member entityMembership, data draftInput, reason string) (int64, error) {
	values, err := validateDraft(tx, member, data)
	if err != nil {
		return 0, err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	var id int64
	err = tx.QueryRow(`INSERT INTO documents(entity_id,outlet_id,supplier_id,issuer_cuit,document_type,
		letter,point_of_sale,number,issue_date,due_date,currency,subtotal_minor,tax_minor,total_minor,description,
		status,revision,created_at,updated_at,created_by_id,updated_by_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'draft',1,$16,$17,$18,$19) RETURNING id`,
		member.ID, *data.Outlet, *data.Supplier, values.CUIT, *data.Kind, values.Letter, *data.Point, *data.Number,
		*data.Issue, data.Due, *data.Currency, values.Net, values.Tax, values.Total, trimDraftText(data.Description),
		at, at, member.userID, member.userID).Scan(&id)
	if isDocumentIdentityConflict(err) {
		return 0, duplicateDocument
	}
	if err != nil {
		return 0, err
	}
	if err := insertDocumentEvent(tx, id, member.userID, "create", reason, at); err != nil {
		return 0, err
	}
	return id, nil
}
