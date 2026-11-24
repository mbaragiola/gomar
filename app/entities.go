package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

type roleChoice struct {
	Value string `json:"valor"`
	Name  string `json:"nombre"`
}

var roles = []roleChoice{
	{"unassigned", "Sin rol"}, {"administration", "Administración"}, {"purchasing", "Compras"},
	{"accounting", "Contabilidad"}, {"read_only", "Consulta"},
}

// Keep capability lists sorted, as in purchases/api.py:entities.
var capabilities = map[string][]string{
	"administration": {
		"download_attachments", "export_financial_reports", "manage_draft_documents", "manage_entity_access",
		"manage_reviewed_documents", "manage_suppliers", "read_business_data", "reconcile_financial_data",
		"record_supplier_payments", "reverse_supplier_payments", "review_documents",
	},
	"purchasing": {"download_attachments", "manage_draft_documents", "manage_suppliers", "read_business_data"},
	"accounting": {
		"download_attachments", "export_financial_reports", "read_business_data", "record_supplier_payments",
		"reverse_supplier_payments", "review_documents",
	},
	"read_only": {"read_business_data"},
}

func roleLabel(value string) string {
	for _, role := range roles {
		if role.Value == value {
			return role.Name
		}
	}
	return ""
}

type entity struct {
	ID   int64  `json:"id"`
	Name string `json:"nombre"`
	CUIT string `json:"cuit"`
}

type entityMembership struct {
	entity
	id, userID int64
	role       string
	employee   bool // Current authenticated browser principal, not a role.
}

type scopedHandler func(*http.Request, *sql.Tx, entityMembership, []byte) (int, any, error)

var (
	missingEntity = problem{404, "No se encontró la entidad."}
	missingOutlet = problem{404, "No se encontró el local."}
	forbiddenRole = problem{403, "Tu rol no permite realizar esta acción."}
)

func (a *app) scoped(capability string, next scopedHandler) http.HandlerFunc {
	return a.scopedRequest(capability, next, false)
}

func (a *app) scopedMutation(next scopedHandler) http.HandlerFunc {
	return a.scopedRequest("manage_entity_access", next, true)
}

func scopedMembership(tx *sql.Tx, userID, entityID int64, capability string) (entityMembership, error) {
	m := entityMembership{entity: entity{ID: entityID}, userID: userID}
	err := tx.QueryRow(`SELECT m.id,m.role,e.name,e.cuit FROM memberships m JOIN entities e ON e.id=m.entity_id
		WHERE m.user_id=$1 AND m.entity_id=$2 AND m.active=TRUE AND e.active=TRUE`, userID, entityID).
		Scan(&m.id, &m.role, &m.Name, &m.CUIT)
	if errors.Is(err, sql.ErrNoRows) {
		return m, missingEntity
	}
	if err == nil && capability != "" && !slices.Contains(capabilities[m.role], capability) {
		return m, forbiddenRole
	}
	return m, err
}

func (a *app) scopedRequest(capability string, next scopedHandler, readBody bool) http.HandlerFunc {
	return a.protected(func(w http.ResponseWriter, r *http.Request, u user) error {
		id, err := pathID(r, "entity")
		if err != nil {
			return err
		}
		var requestBody []byte
		var bodyErr error
		if readBody {
			requestBody, bodyErr = readJSONBody(w, r)
		}
		tx, err := a.beginEmployeeCheckedWrite(r, u)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		m, err := scopedMembership(tx, u.id, id, capability)
		if err != nil {
			return err
		}
		if capability == "manage_entity_access" && readBody {
			if err := lockPostgresEntity(r.Context(), tx, id); errors.Is(err, sql.ErrNoRows) {
				return missingEntity
			} else if err != nil {
				return err
			}
			m, err = scopedMembership(tx, u.id, id, capability)
			if err != nil {
				return err
			}
		}
		if bodyErr != nil {
			return bodyErr
		}
		m.employee = u.employee
		status, body, err := next(r, tx, m, requestBody)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		jsonResponse(w, status, body)
		return nil
	})
}

func (a *app) entities(w http.ResponseWriter, r *http.Request, u user) error {
	type item struct {
		entity
		Role         string   `json:"rol"`
		RoleLabel    string   `json:"rol_nombre"`
		Capabilities []string `json:"capacidades"`
	}
	ctx := r.Context()
	var querier discoveryReadQuerier = a.db
	var tx *sql.Tx
	var err error
	query := `SELECT e.id,e.name,e.cuit,m.role,
		$2 AND EXISTS(SELECT 1 FROM mcp_proposal_sessions k
			JOIN sessions s ON s.token_hash=k.login_hash AND s.user_id=k.actor_id AND s.password_digest=k.password_digest
			WHERE k.actor_id=m.user_id AND k.entity_id=e.id AND k.expires_at>$3 AND k.revoked_at IS NULL AND s.expires>$4)
		FROM memberships m JOIN entities e ON e.id=m.entity_id
		WHERE m.user_id=$1 AND m.active=TRUE AND e.active=TRUE AND ($5=0 OR e.id=$5)`
	args := []any{u.id, a.posRenameProposalsEnabled() || a.documentReviewProposalsEnabled() || a.stockAdjustmentProposalsEnabled(),
		a.tokenTime().Unix(), time.Now().Unix(), u.employeeOrg}
	if a.hostedDiscoveryRead != nil {
		tx, err = a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		scopes, err := a.hostedDiscoveryRead.discover(ctx, tx, u.id,
			sql.NullInt64{Int64: u.employeeOrg, Valid: u.employeeOrg != 0}, sql.NullInt64{}, true)
		if err != nil {
			return err
		}
		ids := make([]int64, 0, len(scopes))
		for id := range scopes {
			ids = append(ids, id)
		}
		query += ` AND e.id=ANY($6::bigint[])`
		args = append(args, ids)
		querier = tx
	}
	rows, err := querier.QueryContext(ctx, query+` ORDER BY e.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	result := []item{}
	for rows.Next() {
		var entry item
		var manageProposalSessions bool
		if err := rows.Scan(&entry.ID, &entry.Name, &entry.CUIT, &entry.Role, &manageProposalSessions); err != nil {
			return err
		}
		entry.Capabilities = capabilities[entry.Role]
		if a.managedEmployeesEnabled() && !u.employee && slices.Contains(entry.Capabilities, "manage_entity_access") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "manage_organization_employees")
			slices.Sort(entry.Capabilities)
		}
		if a.enableMCP && slices.Contains(entry.Capabilities, "read_business_data") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_read")
			slices.Sort(entry.Capabilities)
		}
		if a.enableMCPWikiProposals && entry.Role == "administration" {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_wiki_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.enableMCPDocumentProposals && slices.Contains(entry.Capabilities, draftCapability) {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_document_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.enableMCPWikiEditProposals && entry.Role == "administration" && slices.Contains(entry.Capabilities, wikiEditCapability) {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_wiki_edit_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.stockReceiptProposalsEnabled() && hasStockReceiptRole(entry.Role) {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_stock_receipt_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.posRenameProposalsEnabled() && entry.Role == "administration" && slices.Contains(entry.Capabilities, "manage_entity_access") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_pos_rename_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.documentReviewProposalsEnabled() && slices.Contains(entry.Capabilities, "review_documents") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_document_review_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.reconciliationDraftProposalsEnabled() && slices.Contains(entry.Capabilities, "reconcile_financial_data") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_reconciliation_draft_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.documentPointOfSaleProposalsEnabled() && slices.Contains(entry.Capabilities, draftCapability) {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_document_draft_point_of_sale_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.documentNumberProposalsEnabled() && slices.Contains(entry.Capabilities, draftCapability) {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_document_draft_number_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.documentEditProposalsEnabled() && slices.Contains(entry.Capabilities, draftCapability) {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_document_edit_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.stockAdjustmentProposalsEnabled() && hasStockReceiptRole(entry.Role) {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_stock_adjustment_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.posCatalogProposalsEnabled() && entry.Role == "administration" && slices.Contains(entry.Capabilities, "manage_entity_access") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_pos_catalog_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.outletDisplayLabelProposalsEnabled() && entry.Role == "administration" && slices.Contains(entry.Capabilities, "manage_entity_access") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_outlet_display_label_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.reservationPolicyNoticeProposalsEnabled() && entry.Role == "administration" {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_reservation_policy_notice_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.reservationSlotAddProposalsEnabled() && entry.Role == "administration" {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_reservation_slot_add_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.reservationTableAddProposalsEnabled() && entry.Role == "administration" {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_reservation_table_add_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.reservationConfirmProposalsEnabled() && entry.Role == "administration" {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_reservation_confirm_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.posCreateProposalsEnabled() && entry.Role == "administration" && slices.Contains(entry.Capabilities, "manage_entity_access") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_pos_create_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.reservationCancelProposalsEnabled() && entry.Role == "administration" {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_reservation_cancel_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.stockProductSetupProposalsEnabled() && entry.Role == "administration" && slices.Contains(entry.Capabilities, "manage_entity_access") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_stock_product_setup_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.stockTrackingProposalsEnabled() && entry.Role == "administration" && slices.Contains(entry.Capabilities, "manage_entity_access") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_stock_tracking_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.stockMinimumProposalsEnabled() && entry.Role == "administration" && slices.Contains(entry.Capabilities, "manage_entity_access") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_stock_minimum_proposals")
			slices.Sort(entry.Capabilities)
		}
		if a.supplierNameProposalsEnabled() && slices.Contains(entry.Capabilities, "manage_suppliers") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_supplier_legal_name_proposals")
			slices.Sort(entry.Capabilities)
		}
		if manageProposalSessions {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "mcp_proposal_session_management")
			slices.Sort(entry.Capabilities)
		}
		if a.enableDelivery && slices.Contains(entry.Capabilities, "read_business_data") {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "read_delivery_orders")
			slices.Sort(entry.Capabilities)
		}
		if a.enablePOS && a.enableStock && slices.Contains([]string{"administration", "purchasing", "accounting"}, entry.Role) {
			entry.Capabilities = append(slices.Clone(entry.Capabilities), "read_pos_stock")
			slices.Sort(entry.Capabilities)
		}
		if len(entry.Capabilities) != 0 {
			entry.RoleLabel = roleLabel(entry.Role)
			if u.employee {
				entry.Capabilities = slices.DeleteFunc(slices.Clone(entry.Capabilities), func(name string) bool { return strings.HasPrefix(name, "mcp_") })
			}
			result = append(result, entry)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	jsonResponse(w, 200, result)
	return nil
}

type outlet struct {
	ID   int64  `json:"id"`
	Name string `json:"nombre"`
}

type managedOutlet struct {
	outlet
	Active     bool `json:"activa"`
	Authorized bool `json:"autorizado"`
}

func listOutlets(_ *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	rows, err := tx.Query(`SELECT o.id,o.name FROM outlets o JOIN outlet_grants g ON o.id=g.outlet_id
		WHERE g.membership_id=$1 AND o.entity_id=$2 AND o.active=TRUE ORDER BY o.name,o.id`, m.id, m.ID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	outlets := []outlet{}
	for rows.Next() {
		var item outlet
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			return 0, nil, err
		}
		outlets = append(outlets, item)
	}
	return 200, outlets, rows.Err()
}

func grantScope(tx *sql.Tx, membershipID, entityID int64) (ids []int64, valid bool, err error) {
	rows, err := tx.Query(`SELECT o.id,o.entity_id FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
		WHERE g.membership_id=$1 ORDER BY o.name,o.id`, membershipID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	ids, valid = []int64{}, true
	for rows.Next() {
		var id, owner int64
		if err := rows.Scan(&id, &owner); err != nil {
			return nil, false, err
		}
		if owner != entityID {
			valid = false
			continue
		}
		ids = append(ids, id)
	}
	return ids, valid, rows.Err()
}

func containsAll(allowed, requested []int64) bool {
	for _, id := range requested {
		if !slices.Contains(allowed, id) {
			return false
		}
	}
	return true
}

type managedMember struct {
	ID       int64   `json:"id"`
	Username string  `json:"usuario"`
	Role     string  `json:"rol"`
	Label    string  `json:"rol_nombre"`
	Active   bool    `json:"activa"`
	Editable bool    `json:"editable"`
	Outlets  []int64 `json:"outlet_ids"`
	userID   int64
	super    bool
}

func (item *managedMember) applyScope(actor entityMembership, scope, grants []int64, valid bool) {
	item.Label = roleLabel(item.Role)
	item.Editable = item.userID != actor.userID && !item.super && valid && containsAll(scope, grants)
	item.Outlets = []int64{}
	for _, id := range grants {
		if slices.Contains(scope, id) {
			item.Outlets = append(item.Outlets, id)
		}
	}
}

func management(_ *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	scope, _, err := grantScope(tx, m.id, m.ID)
	if err != nil {
		return 0, nil, err
	}
	outlets, err := managementOutlets(tx, m.ID, scope)
	if err != nil {
		return 0, nil, err
	}
	members, err := managementMembers(tx, m, scope)
	return 200, map[string]any{"entidad": m.entity, "roles": roles, "locales": outlets, "membresias": members}, err
}

func managementOutlets(tx *sql.Tx, entityID int64, scope []int64) ([]managedOutlet, error) {
	rows, err := tx.Query("SELECT id,name,active FROM outlets WHERE entity_id=$1 ORDER BY name,id", entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []managedOutlet{}
	for rows.Next() {
		var item managedOutlet
		if err := rows.Scan(&item.ID, &item.Name, &item.Active); err != nil {
			return nil, err
		}
		item.Authorized = slices.Contains(scope, item.ID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func managementMembers(tx *sql.Tx, m entityMembership, scope []int64) ([]managedMember, error) {
	rows, err := tx.Query(`SELECT m.id,u.id,u.username,u.superuser,m.role,m.active FROM memberships m
		JOIN users u ON u.id=m.user_id WHERE m.entity_id=$1
 AND u.password NOT LIKE '!employee-v1-%' ORDER BY u.username`, m.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []managedMember{}
	for rows.Next() {
		var item managedMember
		if err := rows.Scan(&item.ID, &item.userID, &item.Username, &item.super, &item.Role, &item.Active); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		grants, valid, err := grantScope(tx, items[i].ID, m.ID)
		if err != nil {
			return nil, err
		}
		items[i].applyScope(m, scope, grants, valid)
	}
	return items, nil
}

func updateEntity(_ *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	var input struct {
		Name *string `json:"nombre"`
		CUIT *string `json:"cuit"`
	}
	if err := decodeJSON(body, &input); err != nil {
		return 0, nil, err
	}
	if input.Name == nil || input.CUIT == nil {
		return 0, nil, badInput
	}
	m.Name = strings.TrimSpace(*input.Name)
	if m.Name == "" {
		return 0, nil, problem{422, "La razón social es obligatoria."}
	}
	var err error
	m.CUIT, err = normalizeCUIT(*input.CUIT)
	if err != nil {
		return 0, nil, err
	}
	_, err = tx.Exec("UPDATE entities SET name=$1,cuit=$2 WHERE id=$3", m.Name, m.CUIT, m.ID)
	if isUnique(err) {
		err = problem{409, "Ya existe una entidad con ese CUIT."}
	}
	return 200, m.entity, err
}

func normalizeCUIT(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	var digits strings.Builder
	for _, ch := range raw {
		switch {
		case ch >= '0' && ch <= '9':
			digits.WriteRune(ch)
		case ch == ' ', ch == '.', ch == '-':
		default:
			return "", problem{422, "El CUIT solo puede contener dígitos, espacios, puntos o guiones."}
		}
	}
	if raw == "" {
		return "", problem{422, "El CUIT solo puede contener dígitos, espacios, puntos o guiones."}
	}
	cuit := digits.String()
	if len(cuit) != 11 {
		return "", problem{422, "El CUIT debe tener 11 dígitos."}
	}
	sum := 0
	for i, weight := range []int{5, 4, 3, 2, 7, 6, 5, 4, 3, 2} {
		sum += int(cuit[i]-'0') * weight
	}
	check := (11 - sum%11) % 11
	if check == 10 {
		check = 9
	}
	if check != int(cuit[10]-'0') {
		return "", problem{422, "El CUIT no es válido."}
	}
	return cuit, nil
}

func saveOutlet(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	var input struct {
		Name   *string `json:"nombre"`
		Active *bool   `json:"activa"`
	}
	active := true
	input.Active = &active
	if err := decodeJSON(body, &input); err != nil {
		return 0, nil, err
	}
	if input.Name == nil || input.Active == nil {
		return 0, nil, badInput
	}
	item := managedOutlet{outlet: outlet{Name: strings.TrimSpace(*input.Name)}, Active: *input.Active}
	update := r.Method == http.MethodPut
	if update {
		var err error
		item.ID, err = pathID(r, "outlet")
		if err != nil {
			return 0, nil, err
		}
		scope, _, err := grantScope(tx, m.id, m.ID)
		if err != nil {
			return 0, nil, err
		}
		if !slices.Contains(scope, item.ID) {
			return 0, nil, missingOutlet
		}
		item.Authorized = true
	}
	if item.Name == "" {
		return 0, nil, problem{422, "El nombre del local es obligatorio."}
	}
	var err error
	status := 200
	if update {
		_, err = tx.Exec("UPDATE outlets SET name=$1,active=$2 WHERE id=$3 AND entity_id=$4", item.Name, item.Active, item.ID, m.ID)
	} else {
		status = 201
		err = tx.QueryRow("INSERT INTO outlets(entity_id,name,active) VALUES ($1,$2,$3) RETURNING id", m.ID, item.Name, item.Active).Scan(&item.ID)
	}
	if isUnique(err) {
		err = problem{409, "Ya existe un local con ese nombre en la entidad."}
	}
	return status, item, err
}

type accessSnapshot struct {
	Role    string  `json:"role"`
	Active  bool    `json:"active"`
	Outlets []int64 `json:"outlet_ids"`
}

func saveMembership(r *http.Request, tx *sql.Tx, m entityMembership, body []byte) (int, any, error) {
	var input struct {
		Username *string `json:"usuario"`
		Role     *string `json:"rol"`
		Active   *bool   `json:"activa"`
		Outlets  []int64 `json:"outlet_ids"`
	}
	create := r.Method == http.MethodPost
	if create {
		role, active := "unassigned", true
		input.Role, input.Active, input.Outlets = &role, &active, []int64{}
	}
	if err := decodeJSON(body, &input); err != nil {
		return 0, nil, err
	}
	if input.Role == nil || input.Active == nil || input.Outlets == nil || (create && input.Username == nil) {
		return 0, nil, badInput
	}
	if roleLabel(*input.Role) == "" {
		return 0, nil, problem{422, "El rol no es válido."}
	}
	scope, _, err := grantScope(tx, m.id, m.ID)
	if err != nil {
		return 0, nil, err
	}
	slices.Sort(input.Outlets)
	requested := slices.Compact(input.Outlets)
	if len(requested) != len(input.Outlets) || !containsAll(scope, requested) {
		return 0, nil, problem{403, "Solo podés asignar los locales que tenés autorizados."}
	}
	item, err := targetMember(r, tx, m.ID, input.Username)
	if err != nil {
		return 0, nil, err
	}
	if item.userID == m.userID {
		return 0, nil, problem{403, "No podés modificar tu propia membresía."}
	}
	if item.super {
		return 0, nil, problem{403, "Las cuentas de plataforma se administran fuera de la entidad."}
	}
	status, action, before := 201, "create_membership", any(struct{}{})
	if create {
		err := tx.QueryRow("INSERT INTO memberships(user_id,entity_id,role,active) VALUES ($1,$2,$3,$4) RETURNING id",
			item.userID, m.ID, *input.Role, *input.Active).Scan(&item.ID)
		if isUnique(err) {
			return 0, nil, problem{409, "El usuario ya tiene una membresía en esta entidad."}
		}
		if err != nil {
			return 0, nil, err
		}
	} else {
		old, valid, err := grantScope(tx, item.ID, m.ID)
		if err != nil {
			return 0, nil, err
		}
		if !valid {
			return 0, nil, problem{409, "La membresía tiene un permiso de local fuera de la entidad."}
		}
		if !containsAll(scope, old) {
			return 0, nil, problem{403, "No podés modificar una membresía con locales fuera de tu alcance."}
		}
		slices.Sort(old)
		status, action, before = 200, "update_membership", accessSnapshot{item.Role, item.Active, old}
		if _, err := tx.Exec("UPDATE memberships SET role=$1,active=$2 WHERE id=$3", *input.Role, *input.Active, item.ID); err != nil {
			return 0, nil, err
		}
		for _, id := range old {
			if !slices.Contains(requested, id) {
				if _, err := tx.Exec("DELETE FROM outlet_grants WHERE membership_id=$1 AND outlet_id=$2", item.ID, id); err != nil {
					return 0, nil, err
				}
			}
		}
	}
	for _, id := range requested {
		if _, err := tx.Exec(`INSERT INTO outlet_grants(membership_id,outlet_id) VALUES ($1,$2)
			ON CONFLICT(membership_id,outlet_id) DO NOTHING`, item.ID, id); err != nil {
			return 0, nil, err
		}
	}
	item.Role, item.Active = *input.Role, *input.Active
	// The snapshots only contain strings, booleans and integer IDs.
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(accessSnapshot{item.Role, item.Active, requested})
	if _, err := tx.Exec(`INSERT INTO access_events(entity_id,membership_id,actor_id,action,before,after,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, m.ID, item.ID, m.userID, action, string(beforeJSON), string(afterJSON), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return 0, nil, err
	}
	grants, valid, err := grantScope(tx, item.ID, m.ID)
	if err != nil {
		return 0, nil, err
	}
	item.applyScope(m, scope, grants, valid)
	return status, item, nil
}

func targetMember(r *http.Request, tx *sql.Tx, entityID int64, username *string) (managedMember, error) {
	var item managedMember
	if r.Method == http.MethodPost {
		err := tx.QueryRow("SELECT id,username,superuser FROM users u WHERE username=$1 AND u.password NOT LIKE '!employee-v1-%'", *username).
			Scan(&item.userID, &item.Username, &item.super)
		if errors.Is(err, sql.ErrNoRows) {
			return item, problem{404, "No se encontró el usuario."}
		}
		return item, err
	}
	id, err := pathID(r, "member")
	if err != nil {
		return item, err
	}
	err = tx.QueryRow(`SELECT m.id,u.id,u.username,u.superuser,m.role,m.active FROM memberships m
		JOIN users u ON u.id=m.user_id WHERE m.id=$1 AND m.entity_id=$2
  AND u.password NOT LIKE '!employee-v1-%'`, id, entityID).
		Scan(&item.ID, &item.userID, &item.Username, &item.super, &item.Role, &item.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return item, problem{404, "No se encontró la membresía."}
	}
	return item, err
}

type supplier struct {
	ID   int64  `json:"id"`
	Name string `json:"razon_social"`
	CUIT string `json:"cuit"`
}

func requireSupplierOutlet(tx *sql.Tx, m entityMembership) error {
	var hasOutlet bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM outlet_grants g JOIN outlets o ON o.id=g.outlet_id
		WHERE g.membership_id=$1 AND o.entity_id=$2 AND o.active=TRUE)`, m.id, m.ID).Scan(&hasOutlet)
	if err != nil {
		return err
	}
	if !hasOutlet {
		return problem{403, "Necesitás al menos un local autorizado para realizar esta acción."}
	}
	return nil
}

func listSuppliers(_ *http.Request, tx *sql.Tx, m entityMembership, _ []byte) (int, any, error) {
	if err := requireSupplierOutlet(tx, m); err != nil {
		return 0, nil, err
	}
	rows, err := tx.Query("SELECT id,name,cuit FROM suppliers WHERE entity_id=$1 AND active=TRUE ORDER BY name,id", m.ID)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	items := []supplier{}
	for rows.Next() {
		var item supplier
		if err := rows.Scan(&item.ID, &item.Name, &item.CUIT); err != nil {
			return 0, nil, err
		}
		items = append(items, item)
	}
	return 200, items, rows.Err()
}
