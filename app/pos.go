package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mbaragiola/gomar/app/internal/postime"
)

const posPath = "/api/v1/entities/{entity}/outlets/{outlet}/pos"
const posCatalogMax = 500
const posItemsMax = 80
const posPriceMax = 100_000_000
const posTotalMax = 10_000_000_000

var posRequestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var posMissingProduct = problem{404, "No se encontró el producto."}
var posChangedCatalog = problem{409, "El producto cambió. Volvé a abrirlo antes de guardar."}
var posConflict = problem{409, "No se puede volver a usar el identificador de esta venta."}
var posWrongActor = problem{403, "Esta venta está asociada a otra sesión de usuario."}
var posUnavailablePrice = problem{422, "La venta incluye una versión de producto no disponible para este local."}

type posProduct struct {
	ID       int64  `json:"id"`
	Name     string `json:"nombre"`
	Price    int64  `json:"precio_centavos"`
	Revision int64  `json:"revision"`
	Active   bool   `json:"activo"`
	Barcode  string `json:"codigo_barras,omitempty"`
}

type posLine struct {
	ProductID int64 `json:"producto_id"`
	Revision  int64 `json:"revision"`
	Quantity  int64 `json:"cantidad"`
}

type posSaleItem struct {
	posLine
	Name  string `json:"nombre"`
	Price int64  `json:"precio_centavos"`
}

type posSale struct {
	PaymentMethod  string        `json:"metodo_pago"`
	ID             int64         `json:"id"`
	RequestID      string        `json:"id_solicitud"`
	Total          int64         `json:"total_centavos"`
	Items          []posSaleItem `json:"items"`
	Recorded       string        `json:"creado_en"`
	TillReported   string        `json:"hora_informada_caja_utc,omitempty"`
	TillTimeStatus string        `json:"estado_hora_caja,omitempty"`
}

func (a *app) registerPOS(add func(string, string, http.HandlerFunc)) {
	if !a.enablePOS {
		return
	}
	sell := func(next scopedHandler) http.HandlerFunc {
		return a.scopedRequest("read_business_data", lockPOSWriteScope("read_business_data", true, next), true)
	}
	manage := func(next scopedHandler) http.HandlerFunc {
		return a.scopedRequest("manage_entity_access", lockPOSWriteScope("manage_entity_access", false, next), true)
	}
	add(posPath+"/catalog", "GET", a.scoped("read_business_data", a.listPOSCatalog))
	if a.enablePOSTableChecks {
		add(posPath+"/table-checks", http.MethodPost, sell(a.savePOSTableCheck))
		add(posPath+"/table-checks/{table}", http.MethodGet, a.scoped("read_business_data", a.readPOSTableCheck))
		add(posPath+"/table-checks/{table}/retire", http.MethodPost, sell(a.retirePOSTableCheck))
		if a.enablePOSManualTender {
			if a.enablePOSTableFinalization {
				add(posPath+"/table-checks/{table}/finalize", http.MethodPost, a.scopedRequest("read_business_data", a.lockPOSTenderForWrite(a.finalizePOSTable), true))
			}
			add(posPath+"/table-checks/{table}/tender", http.MethodGet, a.scoped("read_business_data", a.readPOSTender))
			add(posPath+"/table-checks/{table}/tender", http.MethodPost, a.scopedRequest("read_business_data", a.lockPOSTenderForWrite(a.writePOSTender), true))
		}
	}
	add(posPath+"/catalog", "POST", manage(savePOSProduct))
	add(posPath+"/catalog/{product}", "PUT", manage(savePOSProduct))
	barcode := manage(a.savePOSBarcode)
	if a.hostedDiscoveryRead != nil {
		barcode = a.scopedRequestPrelock("manage_entity_access", lockPOSWriteScope("manage_entity_access", false, a.savePOSBarcode), true, lockHostedNativeBarcodeBrowser)
	}
	add(posPath+"/catalog/{product}/barcode", "PUT", barcode)
	add(posPath+"/sales", "POST", sell(func(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
		return postPOSSale(r, tx, m, raw, a.enableStock, a.posTime)
	}))
	add(posPath+"/sale-times/{request}", http.MethodGet, a.scoped("read_business_data", a.readPOSSaleTime))
}

func posOutlet(r *http.Request, tx *sql.Tx, m entityMembership) (int64, error) {
	outlet, err := pathID(r, "outlet")
	if err != nil {
		return 0, err
	}
	return outlet, requireSelectedDocumentOutlet(tx, m, outlet)
}

const currentOutletPOSProducts = `SELECT p.id,v.name,v.price_minor,v.revision,v.active,COALESCE(p.barcode,'')
 FROM pos_products p JOIN pos_product_versions v ON v.product_id=p.id AND v.outlet_id=p.outlet_id
 AND v.revision=p.current_revision WHERE p.outlet_id=$1`

func (a *app) listPOSCatalog(r *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(r.Context(), currentOutletPOSProducts+` ORDER BY p.id LIMIT $2`, outlet, posCatalogMax)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	products := []posProduct{}
	for rows.Next() {
		var p posProduct
		if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Revision, &p.Active, &p.Barcode); err != nil {
			return 0, nil, err
		}
		products = append(products, p)
	}
	return 200, struct {
		Actor        int64        `json:"actor_id"`
		Products     []posProduct `json:"productos"`
		TableChecks  bool         `json:"cuentas_mesa,omitempty"`
		ManualTender bool         `json:"registro_medios_pago,omitempty"`
		Finalization bool         `json:"finalizacion_mesa,omitempty"`
		TillTimeV1   bool         `json:"hora_informada_caja_utc_v1,omitempty"`
	}{
		Actor: m.userID, Products: products, TableChecks: a.enablePOSTableChecks && posSellingRole(m.role) && !m.employee,
		ManualTender: a.enablePOSManualTender && a.enablePOSTableChecks && posSellingRole(m.role) && !m.employee,
		Finalization: a.enablePOSTableFinalization && a.enablePOSManualTender && a.enablePOSTableChecks && posSellingRole(m.role) && !m.employee,
		TillTimeV1:   posSellingRole(m.role),
	}, rows.Err()
}

func savePOSProduct(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte) (int, any, error) {
	ctx := r.Context()
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	var input struct {
		Name     string `json:"nombre"`
		Price    int64  `json:"precio_centavos"`
		Active   *bool  `json:"activo"`
		Revision int64  `json:"revision"`
	}
	create := r.Method == "POST"
	fields := []string{"nombre", "precio_centavos", "activo"}
	if !create {
		fields = append(fields, "revision")
	}
	if err := decodePayables(raw, &input, fields...); err != nil {
		return 0, nil, err
	}
	name := strings.TrimSpace(input.Name)
	if len(name) == 0 || utf8.RuneCountInString(name) > 120 || strings.ContainsRune(name, 0) ||
		input.Price < 1 || input.Price > posPriceMax || input.Active == nil {
		return 0, nil, badInput
	}
	p := posProduct{Name: name, Price: input.Price, Active: *input.Active}
	status := 200
	if create {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pos_products WHERE outlet_id=$1", outlet).Scan(&count); err != nil {
			return 0, nil, err
		}
		if count >= posCatalogMax {
			return 0, nil, problem{422, "El catálogo alcanzó el máximo de productos para este local."}
		}
		status, p.Revision = 201, 1
		if err := tx.QueryRowContext(ctx, `INSERT INTO pos_products(outlet_id,current_revision) VALUES($1,1) RETURNING id`, outlet).Scan(&p.ID); err != nil {
			return 0, nil, err
		}
	} else {
		p.ID, err = pathID(r, "product")
		if err != nil {
			return 0, nil, err
		}
		if input.Revision < 1 {
			return 0, nil, badInput
		}
		var current int64
		err = tx.QueryRowContext(ctx, `SELECT current_revision,COALESCE(barcode,'') FROM pos_products WHERE outlet_id=$1 AND id=$2`, outlet, p.ID).Scan(&current, &p.Barcode)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, posMissingProduct
		}
		if err != nil {
			return 0, nil, err
		}
		if current != input.Revision {
			return 0, nil, posChangedCatalog
		}
		p.Revision = current + 1
		if _, err := tx.ExecContext(ctx, `UPDATE pos_products SET current_revision=$1 WHERE id=$2 AND outlet_id=$3`, p.Revision, p.ID, outlet); err != nil {
			return 0, nil, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pos_product_versions(product_id,outlet_id,revision,name,price_minor,active)
		VALUES($1,$2,$3,$4,$5,$6)`, p.ID, outlet, p.Revision, p.Name, p.Price, p.Active)
	return status, p, err
}

func postPOSSale(r *http.Request, tx *sql.Tx, m entityMembership, raw []byte, stockEnabled bool, now func() time.Time) (int, any, error) {
	if !posSellingRole(m.role) {
		return 0, nil, forbiddenRole
	}
	ctx := r.Context()
	outlet, err := posOutlet(r, tx, m)
	if err != nil {
		return 0, nil, err
	}
	var input struct {
		PaymentMethod json.RawMessage `json:"metodo_pago"`
		RequestID     string          `json:"id_solicitud"`
		Actor         int64           `json:"actor_id"`
		Items         []posLine       `json:"items"`
		TillReported  json.RawMessage `json:"hora_informada_caja_utc"`
	}
	if err := decodePayables(raw, &input, "id_solicitud", "actor_id", "items", "metodo_pago", "hora_informada_caja_utc"); err != nil {
		return 0, nil, err
	}
	method := "efectivo" // Older offline cash carts did not send this field.
	if input.PaymentMethod != nil {
		method = ""
		if err := json.Unmarshal(input.PaymentMethod, &method); err != nil {
			return 0, nil, badInput
		}
		switch method {
		case "debito", "credito", "qr_modo", "qr_mercado_pago", "transferencia", "efectivo":
		default:
			return 0, nil, badInput
		}
	}
	if !posRequestID.MatchString(input.RequestID) || input.Actor < 1 || len(input.Items) < 1 || len(input.Items) > posItemsMax {
		return 0, nil, badInput
	}
	if input.Actor != m.userID {
		return 0, nil, posWrongActor
	}
	slices.SortFunc(input.Items, func(a, b posLine) int {
		if a.ProductID < b.ProductID {
			return -1
		}
		if a.ProductID > b.ProductID {
			return 1
		}
		return 0
	})
	last := int64(0)
	for _, line := range input.Items {
		if line.ProductID <= last || line.Revision < 1 || line.Quantity < 1 || line.Quantity > 999 {
			return 0, nil, badInput
		}
		last = line.ProductID
	}
	sale := posSale{RequestID: input.RequestID, Items: make([]posSaleItem, 0, len(input.Items))}
	var savedOutlet, savedActor int64
	var finalized bool
	err = tx.QueryRowContext(ctx, `SELECT id,outlet_id,actor_id,total_minor,recorded_at,payment_method,COALESCE(till_reported_at_utc,''),EXISTS(SELECT 1 FROM pos_table_finalizations f WHERE f.sale_id=s.id) FROM pos_sales s WHERE request_id=$1`, input.RequestID).
		Scan(&sale.ID, &savedOutlet, &savedActor, &sale.Total, &sale.Recorded, &sale.PaymentMethod, &sale.TillReported, &finalized)
	if err == nil {
		if finalized || savedOutlet != outlet || savedActor != m.userID || sale.PaymentMethod != method {
			return 0, nil, posConflict
		}
		rows, err := tx.QueryContext(ctx, `SELECT i.product_id,i.revision,i.quantity,v.name,v.price_minor
			FROM pos_sale_items i JOIN pos_product_versions v ON v.product_id=i.product_id AND v.outlet_id=i.outlet_id
			AND v.revision=i.revision WHERE i.sale_id=$1 ORDER BY i.product_id`, sale.ID)
		if err != nil {
			return 0, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var item posSaleItem
			if err := rows.Scan(&item.ProductID, &item.Revision, &item.Quantity, &item.Name, &item.Price); err != nil {
				return 0, nil, err
			}
			i := len(sale.Items)
			if i >= len(input.Items) || item.posLine != input.Items[i] {
				return 0, nil, posConflict
			}
			sale.Items = append(sale.Items, item)
		}
		if err := rows.Err(); err != nil {
			return 0, nil, err
		}
		if len(sale.Items) != len(input.Items) {
			return 0, nil, posConflict
		}
		// This is the original evidence (including absence), not the retry's claim.
		if sale.TillReported != "" {
			registered, _ := postime.ParseUTC(sale.Recorded)
			sale.TillTimeStatus, _ = postime.Status(sale.TillReported, registered)
		}
		return 200, sale, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	sale.TillReported, err = parsePOSSaleTillTime(input.TillReported)
	if err != nil {
		return 0, nil, err
	}
	for _, line := range input.Items {
		item := posSaleItem{posLine: line}
		err := tx.QueryRowContext(ctx, `SELECT name,price_minor FROM pos_product_versions
			WHERE outlet_id=$1 AND product_id=$2 AND revision=$3 AND active=TRUE`, outlet, line.ProductID, line.Revision).Scan(&item.Name, &item.Price)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, posUnavailablePrice
		}
		if err != nil {
			return 0, nil, err
		}
		sale.Total += item.Price * line.Quantity
		if sale.Total > posTotalMax {
			return 0, nil, badInput
		}
		sale.Items = append(sale.Items, item)
	}
	sale.PaymentMethod = method
	registered := now().UTC()
	sale.Recorded = registered.Format(time.RFC3339Nano)
	if sale.TillReported != "" {
		sale.TillTimeStatus, _ = postime.Status(sale.TillReported, registered)
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO pos_sales(request_id,outlet_id,actor_id,total_minor,recorded_at,payment_method,till_reported_at_utc)
		VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')) ON CONFLICT(request_id) DO NOTHING RETURNING id`, sale.RequestID, outlet, m.userID, sale.Total, sale.Recorded, method, sale.TillReported).Scan(&sale.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, posConflict
		}
		return 0, nil, err
	}
	for _, item := range sale.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pos_sale_items(sale_id,outlet_id,product_id,revision,quantity)
			VALUES($1,$2,$3,$4,$5)`, sale.ID, outlet, item.ProductID, item.Revision, item.Quantity); err != nil {
			return 0, nil, err
		}
	}
	if stockEnabled {
		if err := recordPOSStockSale(ctx, tx, outlet, sale.ID); err != nil {
			return 0, nil, err
		}
	}
	return 201, sale, nil
}

// Shell assets are deliberately generic. Authentication belongs to the data APIs;
// the worker may load this generic shell without cookies when offline.
func (a *app) registerPOSShell(handle func(string, http.HandlerFunc), frontend http.Handler) {
	if !a.enablePOS {
		return
	}
	for path, file := range map[string]string{"/pos": "/static/pos-shell.html", "/pos-sw.js": "/static/pos-sw.js"} {
		handle("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.NotFound(w, r)
				return
			}
			copy := r.Clone(r.Context())
			copy.URL.Path, copy.URL.RawPath = file, ""
			if path == "/pos-sw.js" {
				w.Header().Set("Service-Worker-Allowed", "/pos")
			}
			w.Header().Set("Cache-Control", "no-store")
			out := posShellResponse{w}
			frontend.ServeHTTP(out, copy)
		})
	}
}

type posShellResponse struct{ http.ResponseWriter }

func (w posShellResponse) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}
