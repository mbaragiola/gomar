<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ApiError, request } from '../api'

type Status = 'pending' | 'approved' | 'rejected' | 'expired' | 'revoked'
type ProductCreate = { name: string; price_minor: number; active: boolean; currency: string; catalog_count: number; catalog_limit: number }
type ReservationTableAdd = { table_number: number; table_label: string; capacity: number; venue_enabled: false }
type StockProductSetup = { product_id: number; expected_revision: number; name: string; minimum: number; tracking_enabled: false }
type StockTracking = { product_id: number; expected_revision: number; name: string; current_tracking_enabled: boolean; tracking_enabled: boolean; minimum: number }
type StockMinimum = { product_id: number; expected_revision: number; name: string; current_minimum: number; minimum: number }
type SupplierName = { supplier_id: number; expected_revision: number; name: string; current_name: string; business_wide: true }
type Proposal = { id: number; status: Status; expires_at: string; product_id?: number; product_revision?: number; rename?: Rename; document_id?: number; document_revision?: number; review?: Review; run_id?: number; draft?: Draft; edit?: DocumentEdit; movement_id?: number; minimum?: number; minimum_change?: StockMinimum; tracking_change?: StockTracking; stock_setup?: StockProductSetup; table_add?: ReservationTableAdd; table_label?: string; capacity?: number; tracking_enabled?: boolean; adjustment?: StockAdjustment; change?: CatalogChange; product?: ProductCreate; supplier_id?: number; supplier_revision?: number; supplier?: SupplierName; reservation_reference?: string; confirmation?: ReservationConfirmation; cancellation?: ReservationCancellation; digest?: string }
type Rename = {
  product_id: number; expected_revision: number; name: string; current_name: string
  unchanged_price_minor: number; unchanged_active: boolean
}
type Review = {
  document_id: number; expected_revision: number; reason: string; type: string; currency: 'ARS' | 'USD'; total_minor: number
}
type DocumentValues = { issue_date: string; due_date: string | null; net_minor: number | null; tax_minor: number | null; total_minor: number }
type DocumentEdit = {
  document_id: number; expected_revision: number; reason: string; currency: 'ARS' | 'USD'; before: DocumentValues; after: DocumentValues
}
type Draft = { match_reference: boolean }
type StockAdjustment = {
  product_id: number; product_name: string; revision: number; stock_minimum: number
  quantity: number; latest_movement: number; before: number; after: number; delta: number
}
type CatalogChange = {
  product_id: number; expected_revision: number; name: string
  current_price_minor: number; price_minor: number; current_active: boolean; active: boolean
}
type ReservationConfirmation = {
  reservation_reference: string; start_utc: string; start_local: string; timezone: string
  party_size: number; status: 'pending'; table_label: string; table_capacity: number
}
type ReservationCancellation = {
  reservation_reference: string; start_utc: string; start_local: string; timezone: string
  party_size: number; status: 'pending' | 'confirmed'; table_label?: string
}
type Action = 'pos_product_rename' | 'document_draft_review' | 'reconciliation_draft_create' | 'document_draft_edit' | 'stock_waste_record' | 'stock_physical_count' | 'pos_product_price_change' | 'pos_product_availability_change' | 'reservation_confirm' | 'reservation_table_add' | 'reservation_cancel' | 'pos_product_create' | 'stock_minimum_change' | 'stock_tracking_change' | 'stock_product_setup' | 'supplier_legal_name_change'
type ScopedProposal = Proposal & { kind: Action }
type Session = { id: number; expires_at: string; actions: string[] }
const actionName = 'pos_product_rename'
const reviewAction = 'document_draft_review'
const draftAction = 'reconciliation_draft_create'
const editAction = 'document_draft_edit'
const wasteAction = 'stock_waste_record'
const countAction = 'stock_physical_count'
const priceAction = 'pos_product_price_change'
const availabilityAction = 'pos_product_availability_change'
const reservationAction = 'reservation_confirm'
const tableAddAction = 'reservation_table_add'
const cancelAction = 'reservation_cancel'
const createAction = 'pos_product_create'
const minimumAction = 'stock_minimum_change'
const trackingAction = 'stock_tracking_change'
const stockSetupAction = 'stock_product_setup'
const supplierAction = 'supplier_legal_name_change'
const isReservation = (action: Action) => action === reservationAction || action === cancelAction
const guardDecisionRetry = (action: Action) => isReservation(action) || action === tableAddAction || action === minimumAction || action === trackingAction || action === stockSetupAction || action === supplierAction
const isCatalog = (action: Action) => action === priceAction || action === availabilityAction
const isStock = (action: Action) => action === wasteAction || action === countAction
const actionLabels: Record<Action, string> = {
  reservation_cancel: 'Cancelar una reserva',
  reservation_confirm: 'Confirmar una reserva pendiente',
  reservation_table_add: 'Preparar una mesa para reservas',
  supplier_legal_name_change: 'Cambiar la razón social de un proveedor',
  stock_minimum_change: 'Cambiar el mínimo de stock',
  stock_tracking_change: 'Cambiar el seguimiento de stock',
  stock_product_setup: 'Configurar un producto para stock',
  pos_product_create: 'Crear un producto de caja',
  pos_product_rename: 'Cambiar el nombre de un producto de caja',
  document_draft_review: 'Marcar un comprobante como revisado',
  reconciliation_draft_create: 'Crear un borrador vacío de conciliación',
  document_draft_edit: 'Editar fechas e importes de un borrador',
  stock_waste_record: 'Registrar una merma de stock',
  stock_physical_count: 'Registrar un conteo físico de stock',
  pos_product_price_change: 'Cambiar el precio de un producto de caja',
  pos_product_availability_change: 'Cambiar la disponibilidad de un producto de caja',
}
const actionUI: Record<Action, {
  path: string; previewSuffix: string; proceed: string; approve: string; confirm: string; rejectConfirm: string
  stale: string; approved: string; rejected: string
}> = {
  reservation_cancel: {
    path: '/reservation-cancel-proposals', previewSuffix: ' de cancelación de reserva', proceed: 'Continuar con la cancelación',
    approve: 'Cancelar reserva definitivamente', confirm: 'Esta cancelación no se puede deshacer. Gomar no avisará a la persona que reservó.',
    rejectConfirm: '¿Querés rechazar la propuesta y dejar la reserva sin cambios?',
    stale: 'No se canceló la reserva. La propuesta o la reserva cambiaron. Revisá el estado antes de preparar otra propuesta.',
    approved: 'Reserva cancelada. No se envió ningún aviso.', rejected: 'Propuesta rechazada. No se modificó la reserva.',
  },
  reservation_table_add: {
    path: '/reservation-table-add-proposals', previewSuffix: ' de mesa para reservas', proceed: 'Continuar con la mesa',
    approve: 'Agregar mesa sin habilitar reservas', confirm: '¿Querés agregar esta mesa al local indicado sin habilitar sus reservas?',
    rejectConfirm: '¿Querés rechazar sin agregar la mesa?',
    stale: 'No se agregó la mesa. La configuración del local o la propuesta cambiaron. Prepará otra propuesta.',
    approved: 'Mesa agregada. Las reservas del local siguen desactivadas.', rejected: 'Propuesta rechazada. No se agregó una mesa.',
  },
  reservation_confirm: {
    path: '/reservation-confirm-proposals', previewSuffix: ' de reserva', proceed: 'Continuar con la reserva',
    approve: 'Confirmar reserva', confirm: '¿Querés confirmar esta reserva para el local, horario y mesa indicados?',
    rejectConfirm: '¿Querés rechazar la propuesta sin modificar la reserva?',
    stale: 'No se confirmó la reserva. La propuesta, la reserva o la mesa cambiaron. Prepará otra propuesta.',
    approved: 'Reserva confirmada.', rejected: 'Propuesta rechazada. No se modificó la reserva.',
  },
  supplier_legal_name_change: {
    path: '/supplier-legal-name-proposals', previewSuffix: ' de razón social', proceed: 'Continuar con el cambio de razón social',
    approve: 'Confirmar nueva razón social', confirm: '¿Querés cambiar la razón social de este proveedor para todos los locales de la empresa indicada?',
    rejectConfirm: '¿Querés rechazar la propuesta sin cambiar el proveedor?',
    stale: 'No se cambió la razón social. El proveedor o la propuesta cambiaron. Prepará otra propuesta.',
    approved: 'Razón social del proveedor actualizada para toda la empresa.', rejected: 'Propuesta rechazada. No se cambió el proveedor.',
  },
  stock_product_setup: {
    path: '/stock-product-setup-proposals', previewSuffix: ' de configuración inicial de stock', proceed: 'Continuar con la configuración inicial',
    approve: 'Crear configuración sin seguimiento', confirm: '¿Querés guardar este mínimo inicial sin activar el seguimiento?',
    rejectConfirm: '¿Querés rechazar sin crear una configuración?',
    stale: 'No se creó la configuración. El producto o la configuración de stock cambiaron. Prepará otra propuesta.',
    approved: 'Configuración creada. El seguimiento sigue desactivado.', rejected: 'Propuesta rechazada. No se creó la configuración.',
  },
  stock_tracking_change: {
    path: '/stock-tracking-proposals', previewSuffix: ' de seguimiento de stock', proceed: 'Continuar con el seguimiento de stock',
    approve: 'Confirmar seguimiento de stock', confirm: '¿Querés cambiar el seguimiento solo para este producto y local?',
    rejectConfirm: '¿Querés rechazar la propuesta sin cambiar el seguimiento?',
    stale: 'No se cambió el seguimiento. El producto o la configuración de stock cambiaron. Prepará otra propuesta.',
    approved: 'Seguimiento de stock actualizado.', rejected: 'Propuesta rechazada. No cambió el seguimiento.',
  },
  stock_minimum_change: {
    path: '/stock-minimum-proposals', previewSuffix: ' de mínimo de stock', proceed: 'Continuar con el mínimo de stock',
    approve: 'Confirmar nuevo mínimo', confirm: '¿Querés cambiar solo el mínimo de stock de este producto en el local indicado?',
    rejectConfirm: '¿Querés rechazar la propuesta y mantener el mínimo actual?',
    stale: 'No se cambió el mínimo. El producto, el mínimo actual o la propuesta cambiaron. Prepará otra propuesta.',
    approved: 'Mínimo de stock actualizado.', rejected: 'Propuesta rechazada. No se cambió el mínimo.',
  },
  pos_product_create: {
    path: '/pos-create-proposals', previewSuffix: ' de nuevo producto', proceed: 'Continuar con el nuevo producto',
    approve: 'Crear producto de caja', confirm: '¿Querés agregar este producto al catálogo del local indicado?',
    rejectConfirm: '¿Querés rechazar sin agregar el producto?',
    stale: 'No se agregó el producto. La propuesta o el catálogo cambiaron. Revisá el estado antes de preparar otra propuesta.',
    approved: 'Producto agregado al catálogo.', rejected: 'Propuesta rechazada. No se agregó ningún producto.',
  },
  pos_product_rename: {
    path: '/pos-rename-proposals', previewSuffix: '', proceed: 'Continuar con este cambio',
    approve: 'Confirmar cambio de nombre', confirm: '¿Querés cambiar solo el nombre de este producto?',
    rejectConfirm: '¿Querés rechazar la propuesta sin modificar el producto?',
    stale: 'No se cambió el nombre. La propuesta o la versión del producto cambiaron.',
    approved: 'Nombre del producto actualizado.', rejected: 'Propuesta rechazada. No se modificó el producto.',
  },
  document_draft_review: {
    path: '/document-review-proposals', previewSuffix: ' de revisión', proceed: 'Continuar con la revisión',
    approve: 'Marcar como revisado', confirm: '¿Querés marcar este comprobante como revisado?',
    rejectConfirm: '¿Querés rechazar sin modificar el comprobante?',
    stale: 'No se marcó como revisado. La propuesta o la versión del comprobante cambiaron.',
    approved: 'Comprobante marcado como revisado.', rejected: 'Propuesta rechazada. No se modificó el comprobante.',
  },
  pos_product_price_change: {
    path: '/pos-price-proposals', previewSuffix: ' de precio', proceed: 'Continuar con el precio',
    approve: 'Confirmar nuevo precio', confirm: '¿Querés guardar únicamente este precio para el local indicado?',
    rejectConfirm: '¿Querés rechazar sin cambiar el precio?',
    stale: 'No se cambió el precio. El producto o la propuesta cambiaron. Prepará otra propuesta.',
    approved: 'Precio actualizado.', rejected: 'Propuesta rechazada. El producto no cambió.',
  },
  pos_product_availability_change: {
    path: '/pos-availability-proposals', previewSuffix: ' de disponibilidad', proceed: 'Continuar con la disponibilidad',
    approve: 'Confirmar disponibilidad', confirm: '¿Querés cambiar únicamente la disponibilidad para el local indicado?',
    rejectConfirm: '¿Querés rechazar sin cambiar la disponibilidad?',
    stale: 'No se cambió la disponibilidad. El producto o la propuesta cambiaron. Prepará otra propuesta.',
    approved: 'Disponibilidad actualizada.', rejected: 'Propuesta rechazada. El producto no cambió.',
  },
  stock_waste_record: {
    path: '/stock-waste-proposals', previewSuffix: ' de merma', proceed: 'Continuar con la merma',
    approve: 'Registrar merma', confirm: '¿Querés registrar esta merma para el local indicado?',
    rejectConfirm: '¿Querés rechazar la merma sin cambiar el stock?',
    stale: 'No se registró la merma. El stock o la propuesta cambiaron. Prepará otra propuesta.',
    approved: 'Merma registrada.', rejected: 'Propuesta rechazada. El stock no cambió.',
  },
  stock_physical_count: {
    path: '/stock-physical-count-proposals', previewSuffix: ' de conteo', proceed: 'Continuar con el conteo',
    approve: 'Registrar conteo físico', confirm: '¿Querés registrar este saldo contado para el local indicado?',
    rejectConfirm: '¿Querés rechazar el conteo sin cambiar el stock?',
    stale: 'No se registró el conteo. El stock o la propuesta cambiaron. Prepará otra propuesta.',
    approved: 'Conteo físico registrado.', rejected: 'Propuesta rechazada. El stock no cambió.',
  },
  document_draft_edit: {
    path: '/document-edit-proposals', previewSuffix: ' de edición', proceed: 'Continuar con la edición',
    approve: 'Guardar fechas e importes', confirm: '¿Querés guardar estas fechas e importes en el borrador?',
    rejectConfirm: '¿Querés rechazar sin modificar el borrador?',
    stale: 'No se guardó la edición. La propuesta o la versión del borrador cambiaron.',
    approved: 'Fechas e importes del borrador guardados.', rejected: 'Propuesta rechazada. No se modificó el borrador.',
  },
  reconciliation_draft_create: {
    path: '/reconciliation-draft-proposals', previewSuffix: ' de conciliación', proceed: 'Continuar con el borrador',
    approve: 'Crear borrador vacío', confirm: '¿Querés crear este borrador vacío para el local indicado?',
    rejectConfirm: '¿Querés rechazar sin crear el borrador?',
    stale: 'No se creó el borrador. La propuesta cambió o ya se resolvió.',
    approved: 'Borrador vacío de conciliación creado.', rejected: 'Propuesta rechazada. No se creó un borrador.',
  },
}
const reservationTimezone = 'America/Argentina/Buenos_Aires'
const reservationWallTime = new Intl.DateTimeFormat('en-GB', {
  timeZone: reservationTimezone, year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
})
const reservationReference = (value: unknown): value is string => typeof value === 'string' && /^[0-9a-f]{32}$/.test(value)
function validReservationStart(x: Pick<ReservationCancellation, 'start_utc' | 'start_local' | 'timezone'>) {
  if (x.timezone !== reservationTimezone ||
    typeof x.start_utc !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(x.start_utc) ||
    typeof x.start_local !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{2}:\d{2}$/.test(x.start_local)) return false
  const utc = new Date(x.start_utc)
  if (!Number.isFinite(utc.getTime()) || utc.toISOString() !== x.start_utc.replace('Z', '.000Z') ||
    Date.parse(x.start_local) !== utc.getTime()) return false
  const parts = Object.fromEntries(reservationWallTime.formatToParts(utc).map(part => [part.type, part.value]))
  return x.start_local.slice(0, 19) === `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}:${parts.second}`
}
function validReservation(x: ReservationConfirmation | undefined): x is ReservationConfirmation {
  return !!x && reservationReference(x.reservation_reference) && x.status === 'pending' &&
    positive(x.party_size) && positive(x.table_capacity) && x.party_size <= x.table_capacity &&
    typeof x.table_label === 'string' && !!x.table_label.trim() && validReservationStart(x)
}
function validReservationTableAdd(x: ReservationTableAdd | undefined): x is ReservationTableAdd {
  return !!x && positive(x.table_number) && x.table_number <= 9999 && x.table_label === `Mesa ${x.table_number}` &&
    positive(x.capacity) && x.capacity <= 12 && x.venue_enabled === false
}
function validReservationCancellation(x: ReservationCancellation | undefined): x is ReservationCancellation {
  return !!x && reservationReference(x.reservation_reference) && positive(x.party_size) &&
    (x.status === 'pending' ? x.table_label === undefined
      : x.status === 'confirmed' && typeof x.table_label === 'string' && !!x.table_label.trim()) &&
    validReservationStart(x)
}
const documentTypes: Record<string, string> = { invoice: 'Factura', credit_note: 'Nota de crédito', debit_note: 'Nota de débito' }
const positive = (value: number) => Number.isSafeInteger(value) && value > 0
function isRename(value: Rename | undefined): value is Rename {
  return !!value && positive(value.product_id) && positive(value.expected_revision) &&
    typeof value.name === 'string' && typeof value.current_name === 'string' &&
    Number.isSafeInteger(value.unchanged_price_minor) && value.unchanged_price_minor >= 0 &&
    typeof value.unchanged_active === 'boolean'
}
function isReview(value: Review | undefined): value is Review {
  return !!value && positive(value.document_id) && Number.isSafeInteger(value.expected_revision) && value.expected_revision >= 0 &&
    typeof value.reason === 'string' && !!value.reason && Object.hasOwn(documentTypes, value.type) &&
    (value.currency === 'ARS' || value.currency === 'USD') &&
    Number.isSafeInteger(value.total_minor) && value.total_minor >= 0
}
const date = (value: unknown) => typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value)
const minor = (value: number) => Number.isSafeInteger(value) && value >= 0 && value <= 99_999_999_999_999
function isDocumentValues(value: DocumentValues | undefined): value is DocumentValues {
  return !!value && date(value.issue_date) && (value.due_date === null || date(value.due_date)) &&
    minor(value.total_minor) && (value.net_minor === null || minor(value.net_minor)) &&
    (value.tax_minor === null || minor(value.tax_minor))
}
function isDocumentEdit(value: DocumentEdit | undefined): value is DocumentEdit {
  return !!value && positive(value.document_id) && Number.isSafeInteger(value.expected_revision) && value.expected_revision >= 0 &&
    typeof value.reason === 'string' && !!value.reason && (value.currency === 'ARS' || value.currency === 'USD') &&
    isDocumentValues(value.before) && isDocumentValues(value.after)
}
const nonnegative = (value: number) => Number.isSafeInteger(value) && value >= 0
function validStock(kind: Action, x: StockAdjustment | undefined): x is StockAdjustment {
  if (!x || !positive(x.product_id) || typeof x.product_name !== 'string' || !nonnegative(x.revision) ||
    !nonnegative(x.stock_minimum) || !nonnegative(x.latest_movement) || !nonnegative(x.quantity) || x.quantity > 1_000_000 ||
    ![x.before, x.after, x.delta].every(Number.isSafeInteger) || x.after - x.before !== x.delta) return false
  return kind === wasteAction ? x.quantity > 0 && x.delta === -x.quantity : x.after === x.quantity
}
const validMinimum = (value: number | undefined): value is number =>
  typeof value === 'number' && nonnegative(value) && value <= 1_000_000
function validStockMinimum(x: StockMinimum | undefined): x is StockMinimum {
  return !!x && positive(x.product_id) && positive(x.expected_revision) &&
    typeof x.name === 'string' && !!x.name.trim() && validMinimum(x.current_minimum) &&
    validMinimum(x.minimum) && x.current_minimum !== x.minimum
}
function validStockProductSetup(x: StockProductSetup | undefined): x is StockProductSetup {
  return !!x && positive(x.product_id) && positive(x.expected_revision) &&
    typeof x.name === 'string' && !!x.name.trim() && validMinimum(x.minimum) && x.tracking_enabled === false
}
function validStockTracking(x: StockTracking | undefined): x is StockTracking {
  return !!x && positive(x.product_id) && positive(x.expected_revision) &&
    typeof x.name === 'string' && !!x.name.trim() && validMinimum(x.minimum) &&
    typeof x.current_tracking_enabled === 'boolean' && typeof x.tracking_enabled === 'boolean' &&
    x.current_tracking_enabled !== x.tracking_enabled
}
const trackingLabel = (enabled: boolean) => enabled ? 'Activado' : 'Desactivado'
function validSupplierName(x: SupplierName | undefined): x is SupplierName {
  return !!x && positive(x.supplier_id) && positive(x.expected_revision) && x.expected_revision < Number.MAX_SAFE_INTEGER &&
    typeof x.current_name === 'string' && typeof x.name === 'string' &&
    !!x.name && !/\p{Cc}/u.test(x.name) && [...x.name].length <= 160 && x.current_name !== x.name &&
    x.business_wide === true
}
const stockUnits = new Intl.NumberFormat('es-AR')
const stockDelta = new Intl.NumberFormat('es-AR', { signDisplay: 'exceptZero' })
function validCatalog(kind: Action, x: CatalogChange | undefined): x is CatalogChange {
  if (!x || !positive(x.product_id) || !positive(x.expected_revision) || typeof x.name !== 'string' ||
    !positive(x.current_price_minor) || x.current_price_minor > 100_000_000 ||
    !positive(x.price_minor) || x.price_minor > 100_000_000 ||
    typeof x.current_active !== 'boolean' || typeof x.active !== 'boolean') return false
  return kind === priceAction
    ? x.current_price_minor !== x.price_minor && x.current_active === x.active
    : x.current_price_minor === x.price_minor && x.current_active !== x.active
}
function validProduct(x: ProductCreate | undefined): x is ProductCreate {
  return !!x && typeof x.name === 'string' && x.name.trim() !== '' && [...x.name].length <= 120 &&
    !x.name.includes('\0') && positive(x.price_minor) && x.price_minor <= 100_000_000 &&
    typeof x.active === 'boolean' && x.currency === 'ARS' && x.catalog_limit === 500 &&
    nonnegative(x.catalog_count) && x.catalog_count < x.catalog_limit
}
const stateLabel = (active: boolean) => active ? 'Activo' : 'Inactivo'
const prices = new Intl.NumberFormat('es-AR', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
const amount = (value: number | null) => value === null ? 'No informado' : prices.format(value / 100)
type Decision = 'approve' | 'reject'
const props = defineProps<{ entity: number; outlet: number; outletName: string; businessName?: string; canRename: boolean; canReviewDocuments?: boolean; canCreateReconciliationDraft?: boolean; canEditDocuments?: boolean; canAdjustStock?: boolean; canChangeCatalog?: boolean; canConfirmReservations?: boolean; canAddReservationTable?: boolean; canCancelReservations?: boolean; canCreateProducts?: boolean; canSetStockMinimum?: boolean; canChangeStockTracking?: boolean; canSetupStockProduct?: boolean; canChangeSupplierName?: boolean }>()
const scopePath = `/entities/${props.entity}/outlets/${props.outlet}`
const base = (kind: Action) => scopePath + actionUI[kind].path
const allowed = (kind: Action) => kind === supplierAction ? !!props.canChangeSupplierName && !!props.businessName?.trim() && !!props.outletName.trim()
  : kind === stockSetupAction ? !!props.canSetupStockProduct && !!props.outletName.trim()
  : kind === trackingAction ? !!props.canChangeStockTracking && !!props.outletName.trim()
  : kind === minimumAction ? !!props.canSetStockMinimum
  : kind === cancelAction ? !!props.canCancelReservations
  : kind === tableAddAction ? !!props.canAddReservationTable && !!props.outletName.trim()
  : kind === reservationAction ? !!props.canConfirmReservations
  : kind === createAction ? !!props.canCreateProducts
  : isCatalog(kind) ? !!props.canChangeCatalog
  : isStock(kind) ? !!props.canAdjustStock
  : kind === editAction ? !!props.canEditDocuments
  : kind === draftAction ? !!props.canCreateReconciliationDraft
  : kind === reviewAction ? !!props.canReviewDocuments : props.canRename
const actionChoices = computed(() => ([actionName, reviewAction, draftAction, editAction, wasteAction, countAction, priceAction, availabilityAction, reservationAction, tableAddAction, cancelAction, createAction, minimumAction, trackingAction, stockSetupAction, supplierAction] as const).filter(allowed))
const setSelected = (kind: Action, event: Event) => {
  if (event.target instanceof HTMLInputElement) selected.value = event.target.checked ? kind : null
}
const actionText = (action: string) => Object.hasOwn(actionLabels, action)
  ? actionLabels[action as Action] : 'Acción no reconocida'
const validPreview = (kind: Action, proposal: Proposal) => kind === supplierAction ? validSupplierName(proposal.supplier)
  : kind === stockSetupAction ? validStockProductSetup(proposal.stock_setup)
  : kind === trackingAction ? validStockTracking(proposal.tracking_change)
  : kind === minimumAction ? validStockMinimum(proposal.minimum_change)
  : kind === cancelAction ? validReservationCancellation(proposal.cancellation)
  : kind === tableAddAction ? validReservationTableAdd(proposal.table_add)
  : kind === reservationAction ? validReservation(proposal.confirmation)
  : kind === createAction ? validProduct(proposal.product)
  : isCatalog(kind) ? validCatalog(kind, proposal.change)
  : isStock(kind) ? validStock(kind, proposal.adjustment)
  : kind === editAction ? isDocumentEdit(proposal.edit)
  : kind === draftAction
  ? typeof proposal.draft?.match_reference === 'boolean'
  : kind === reviewAction ? isReview(proposal.review) : isRename(proposal.rename)

const keyPath = '/auth/mcp-proposal-sessions'
const sessionPath = `/entities/${props.entity}/outlets/${props.outlet}/proposal-sessions`
const selected = ref<Action | null>(null)
const sessions = ref<Session[]>([])
const items = ref<ScopedProposal[]>([])
const viewed = ref<ScopedProposal | null>(null)
const deciding = ref<{ id: number; digest: string; action: Decision; kind: Action } | null>(null)
const opened = ref(false)
const busy = ref(false)
const notice = ref('')
const error = ref('')
const credential = ref<{ id: number; expires: number; kind: Action } | null>(null)
const setupAction = computed(() => credential.value?.kind ?? selected.value)
const available = ref(false)
const revealed = ref('')
let secret = ''
let alive = true
let generation = 0
let disclosure = 0
let expiryTimer: ReturnType<typeof setTimeout> | undefined

const labels: Record<Status, string> = {
  pending: 'Pendiente', approved: 'Aplicada', rejected: 'Rechazada', expired: 'Vencida', revoked: 'Revocada',
}
const displayExpiry = (time: number) => new Intl.DateTimeFormat('es-AR', {
  hour: '2-digit', minute: '2-digit', timeZoneName: 'short',
}).format(time)
const closedScopedDecisions = reactive(new Set<string>())
const decisionKey = (kind: Action, id: number) => `${kind}:${id}`
const cancelAcknowledged = ref(false)
const tableAddAcknowledged = ref(false)
const needsTableAddAcknowledgement = computed(() => deciding.value?.kind === tableAddAction && deciding.value.action === 'approve')
const trackingAcknowledged = ref(false)
const needsTrackingAcknowledgement = computed(() => deciding.value?.kind === trackingAction &&
  deciding.value.action === 'approve' && viewed.value?.tracking_change?.tracking_enabled === false)
const canDecide = computed(() => viewed.value?.status === 'pending' && (!guardDecisionRetry(viewed.value.kind) || !closedScopedDecisions.has(decisionKey(viewed.value.kind, viewed.value.id))) && allowed(viewed.value.kind) && validPreview(viewed.value.kind, viewed.value) &&
  /^[a-f0-9]{64}$/.test(viewed.value.digest ?? ''))

function hideKey() {
  disclosure++
  secret = ''
  revealed.value = ''
  available.value = false
}

function keyDetailsChanged(event: Event) {
  if (event.target instanceof HTMLDetailsElement && !event.target.open) {
    selected.value = null
    hideKey()
  }
}

function clearKey() {
  if (expiryTimer !== undefined) clearTimeout(expiryTimer)
  expiryTimer = undefined
  hideKey()
  credential.value = null
}

function clearPreview() {
  deciding.value = null
  viewed.value = null
}

function dismiss() {
  generation++
  opened.value = false
  busy.value = false
  error.value = notice.value = ''
  items.value = []
  sessions.value = []
  selected.value = null
  clearPreview()
  hideKey()
}

function active(run: number) { return alive && opened.value && run === generation }

async function list(run: number) {
  const got = await request<{ sessions: Session[] }>(sessionPath, { cache: 'no-store' })
  if (!Array.isArray(got.sessions) || !got.sessions.every(s => positive(s.id) &&
    Number.isFinite(Date.parse(s.expires_at)) && Array.isArray(s.actions) && s.actions.every(a => typeof a === 'string'))) {
    throw new Error('unexpected session list')
  }
  if (active(run)) sessions.value = got.sessions
  if (!active(run)) return
  const proposals = await Promise.all(actionChoices.value.map(async kind => {
    const pending = await request<{ proposals: Proposal[] }>(base(kind), { cache: 'no-store' })
    return pending.proposals.map(proposal => ({ ...proposal, kind }))
  }))
  if (active(run)) items.value = proposals.flat().filter(p => allowed(p.kind))
}

async function refresh() {
  if (busy.value) return
  clearPreview()
  await perform('No pude cargar las propuestas.', list)
}

async function toggle() {
  if (opened.value) { dismiss(); return }
  opened.value = true
  await refresh()
}

async function perform(message: string, action: (run: number) => Promise<void>, kind: Action = actionName) {
  if (busy.value) return
  const run = generation
  busy.value = true
  error.value = notice.value = ''
  try {
    await action(run)
  } catch (cause) {
    if (!active(run)) return
    if (cause instanceof ApiError && (cause.status === 409 || cause.status === 410)) {
      if (guardDecisionRetry(kind) && deciding.value) closedScopedDecisions.add(decisionKey(kind, deciding.value.id))
      clearPreview()
      error.value = cause.status === 410 ? 'La propuesta venció o se revocó su clave.' : actionUI[kind].stale
      try { await list(run) } catch { /* Keep the decision error. */ }
    } else {
      if ((kind === cancelAction || kind === tableAddAction || kind === minimumAction || kind === trackingAction || kind === stockSetupAction || kind === supplierAction) && deciding.value) clearPreview()
      error.value = message
    }
  } finally {
    if (active(run)) busy.value = false
  }
}

async function preview(id: number, kind: Action) {
  if (!allowed(kind)) return
  clearPreview()
  await perform('No pude abrir la propuesta.', async run => {
    const got = await request<Proposal>(`${base(kind)}/${id}`, { cache: 'no-store' })
    if (got.id !== id || (got.status === 'pending' &&
      (!validPreview(kind, got) || !/^[a-f0-9]{64}$/.test(got.digest ?? '')))) {
      throw new Error('unexpected proposal response')
    }
    if (active(run) && allowed(kind)) viewed.value = { ...got, kind }
  }, kind)
}

function begin(action: Decision) {
  if (!canDecide.value || busy.value || !viewed.value?.digest) return
  cancelAcknowledged.value = false
  tableAddAcknowledged.value = false
  trackingAcknowledged.value = false
  deciding.value = { id: viewed.value.id, digest: viewed.value.digest, action, kind: viewed.value.kind }
}

async function decide() {
  const target = deciding.value
  const proposed = viewed.value
  if (busy.value || !target || !proposed || !canDecide.value || target.id !== proposed.id ||
    target.digest !== proposed.digest || target.kind !== proposed.kind ||
    (target.kind === cancelAction && target.action === 'approve' && !cancelAcknowledged.value) ||
    (needsTrackingAcknowledgement.value && !trackingAcknowledged.value) ||
    (needsTableAddAcknowledgement.value && !tableAddAcknowledged.value)) return
  if (target.kind === cancelAction || target.kind === tableAddAction || target.kind === minimumAction || target.kind === trackingAction || target.kind === stockSetupAction || target.kind === supplierAction) closedScopedDecisions.add(decisionKey(target.kind, target.id))
  await perform(target.kind === cancelAction
    ? 'No pude verificar el resultado. Revisá el estado en Reservas antes de hacer otro cambio.'
    : target.kind === tableAddAction
      ? 'No pude verificar el resultado. Consultá la configuración de reservas antes de preparar otra mesa.'
      : target.kind === minimumAction
        ? 'No pude verificar el resultado. Revisá el mínimo de stock antes de preparar otra propuesta.'
      : target.kind === stockSetupAction
        ? 'No pude verificar el resultado. Revisá la configuración de stock antes de preparar otra propuesta.'
        : target.kind === trackingAction
          ? 'No pude verificar el resultado. Revisá el seguimiento de stock antes de preparar otra propuesta.'
        : target.kind === supplierAction
          ? 'No pude verificar el resultado. Revisá la razón social en Proveedores antes de preparar otra propuesta.'
        : 'No pude resolver la propuesta.', async run => {
    const result = await request<Proposal>(`${base(target.kind)}/${target.id}/decision`, {
      method: 'POST', cache: 'no-store',
      body: JSON.stringify({ decision: target.action, digest: target.digest, confirm: true,
        ...(needsTrackingAcknowledgement.value ? { acknowledge_no_backfill: trackingAcknowledged.value } : {}),
        ...(needsTableAddAcknowledgement.value ? { acknowledge_no_remove: tableAddAcknowledged.value } : {}) }),
    })
    if (!active(run)) return
    const validApproval = target.kind === supplierAction
      ? result.supplier_id === proposed.supplier?.supplier_id &&
        result.supplier_revision === (proposed.supplier?.expected_revision ?? -1) + 1 && result.supplier === undefined
       : target.kind === stockSetupAction
      ? result.product_id === proposed.stock_setup?.product_id && validMinimum(result.minimum) &&
        result.minimum === proposed.stock_setup?.minimum && result.tracking_enabled === false &&
        result.product_revision === undefined && result.stock_setup === undefined
      : target.kind === trackingAction
      ? result.product_id === proposed.tracking_change?.product_id && typeof result.tracking_enabled === 'boolean' &&
        result.tracking_enabled === proposed.tracking_change?.tracking_enabled && result.product_revision === undefined &&
        result.tracking_change === undefined && result.minimum === undefined
      : target.kind === minimumAction
      ? result.product_id === proposed.minimum_change?.product_id && validMinimum(result.minimum) &&
        result.minimum === proposed.minimum_change?.minimum && result.product_revision === undefined
      : target.kind === tableAddAction
      ? result.table_label === proposed.table_add?.table_label && result.capacity === proposed.table_add?.capacity &&
        result.table_add === undefined && result.reservation_reference === undefined
      : isReservation(target.kind)
      ? reservationReference(result.reservation_reference) &&
        result.reservation_reference === (target.kind === cancelAction ? proposed.cancellation : proposed.confirmation)?.reservation_reference
      : target.kind === createAction ? positive(result.product_id ?? 0) && result.product_revision === 1
      : isCatalog(target.kind)
      ? result.product_id === proposed.change?.product_id && result.product_revision === (proposed.change?.expected_revision ?? -1) + 1
      : isStock(target.kind) ? positive(result.movement_id ?? 0)
      : target.kind === editAction
      ? result.document_id === proposed.edit?.document_id && result.document_revision === (proposed.edit?.expected_revision ?? -1) + 1
      : target.kind === draftAction ? positive(result.run_id ?? 0)
      : target.kind === reviewAction
      ? result.document_id === proposed.review?.document_id && result.document_revision === (proposed.review?.expected_revision ?? -1) + 1
      : result.product_id === proposed.rename?.product_id && result.product_revision === (proposed.rename?.expected_revision ?? -1) + 1
    if (result.id !== target.id || result.status !== (target.action === 'approve' ? 'approved' : 'rejected') ||
      (target.action === 'approve' && !validApproval) ||
      (isReservation(target.kind) && target.action === 'reject' && result.reservation_reference !== undefined) ||
      (target.kind === tableAddAction && target.action === 'reject' &&
        (result.table_label !== undefined || result.capacity !== undefined || result.table_add !== undefined ||
         result.reservation_reference !== undefined)) ||
      (target.kind === stockSetupAction && target.action === 'reject' &&
        (result.product_id !== undefined || result.product_revision !== undefined || result.tracking_enabled !== undefined ||
         result.stock_setup !== undefined || result.minimum !== undefined)) ||
      (target.kind === trackingAction && target.action === 'reject' &&
        (result.product_id !== undefined || result.product_revision !== undefined || result.tracking_enabled !== undefined ||
         result.tracking_change !== undefined || result.minimum !== undefined)) ||
      (target.kind === minimumAction && target.action === 'reject' &&
        (result.product_id !== undefined || result.minimum !== undefined || result.product_revision !== undefined)) ||
      (target.kind === supplierAction && target.action === 'reject' &&
        (result.supplier_id !== undefined || result.supplier_revision !== undefined || result.supplier !== undefined))) {
      if (guardDecisionRetry(target.kind)) {
        closedScopedDecisions.add(decisionKey(target.kind, target.id))
        clearPreview()
      }
      throw new Error('unexpected decision receipt')
    }
    clearPreview()
    items.value = items.value.map(item => item.id === result.id && item.kind === target.kind ? { ...result, kind: target.kind } : item)
    notice.value = result.status === 'approved' ? actionUI[target.kind].approved : actionUI[target.kind].rejected
  }, target.kind)
}

function revokeKey(id: number) {
  return request<void>(`${keyPath}/${id}`, { method: 'DELETE', cache: 'no-store' })
}

async function issue() {
  const chosen = selected.value
  if (credential.value || !chosen || !allowed(chosen)) return
  const disclosureRun = disclosure
  await perform('No pude crear la sesión de propuestas.', async run => {
    const got = await request<Session & { token: string }>(keyPath, {
      method: 'POST', cache: 'no-store', body: JSON.stringify({ entity_id: props.entity, outlet_id: props.outlet, actions: [chosen] }),
    })
    if (!Number.isSafeInteger(got.id) || got.id < 1) throw new Error('unexpected credential id')
    const expires = Date.parse(got.expires_at)
    if (!active(run) || !allowed(chosen) || disclosureRun !== disclosure || !Number.isFinite(expires) || expires <= Date.now() || !Array.isArray(got.actions) ||
      got.actions.length !== 1 || got.actions[0] !== chosen || !/^[A-Za-z0-9_-]{43}$/.test(got.token)) {
      void revokeKey(got.id).catch(() => {})
      throw new Error('discarded credential')
    }
    credential.value = { id: got.id, expires, kind: chosen }
    selected.value = null
    sessions.value = [...sessions.value, { id: got.id, expires_at: got.expires_at, actions: got.actions }]
    available.value = true
    secret = got.token
    expiryTimer = setTimeout(() => {
      clearKey()
      sessions.value = sessions.value.filter(session => session.id !== got.id)
      clearPreview()
      if (opened.value) notice.value = 'La clave venció.'
    }, Math.min(expires - Date.now(), 2_147_483_647))
  })
}

async function copyKey() {
  if (!secret || !credential.value || credential.value.expires <= Date.now()) return
  await perform('No pude copiar la clave. Podés mostrarla una vez y copiarla a mano.', async run => {
    await navigator.clipboard.writeText(secret)
    if (active(run)) {
      hideKey()
      notice.value = 'Clave copiada. Pegala en el archivo privado de tu equipo.'
    }
  })
}

function showKey() {
  if (!secret || !credential.value || credential.value.expires <= Date.now() || busy.value) return
  revealed.value = secret
  secret = ''
  available.value = false
}

async function revoke(id: number) {
  await perform('No pude revocar la clave.', async run => {
    await revokeKey(id)
    if (alive && credential.value?.id === id) clearKey()
    if (!active(run)) return
    clearPreview()
    sessions.value = sessions.value.filter(session => session.id !== id)
    notice.value = 'Sesión revocada. Sus propuestas pendientes no se pueden aprobar.'
    try { await list(run) } catch { /* Revocation still succeeded. */ }
  })
}

watch(actionChoices, availableActions => {
  if (selected.value && !availableActions.includes(selected.value)) selected.value = null
  if (credential.value && !availableActions.includes(credential.value.kind)) clearKey()
  if (viewed.value && !availableActions.includes(viewed.value.kind)) clearPreview()
  items.value = items.value.filter(item => availableActions.includes(item.kind))
})
onMounted(() => window.addEventListener('pagehide', dismiss))
onBeforeUnmount(() => {
  alive = false
  window.removeEventListener('pagehide', dismiss)
  dismiss()
  clearKey()
})
</script>

<template>
  <div class="appearance-toolbar mcp-connect-launch">
    <button type="button" class="secondary" :aria-expanded="opened" aria-controls="pos-rename-proposals" @click="toggle">
      {{ opened ? 'Cerrar sesiones y propuestas' : 'Sesiones de propuestas' }}
    </button>
  </div>
  <section v-if="opened" id="pos-rename-proposals" class="card subsection gomar-feature mcp-connect" aria-labelledby="pos-rename-proposals-heading" :aria-busy="busy">
    <h2 id="pos-rename-proposals-heading">Sesiones de propuestas</h2>
    <p>Claude o Codex pueden preparar solo las acciones que elijas para <strong>{{ outletName }}</strong>. No aplica cambios. Vas a aprobarlos en Gomar.</p>
    <details v-if="actionChoices.length" @toggle="keyDetailsChanged">
      <summary>Qué puede proponer este chat</summary>
      <p>Esta sesión es distinta de la clave de lectura y vence en 15 minutos como máximo. Cerrar el panel u ocultar la clave no termina la sesión.</p>
      <p v-if="credential">Vence a las {{ displayExpiry(credential.expires) }}.</p>
      <template v-if="!credential">
        <p v-if="actionChoices.length > 1">Elegí una acción para esta sesión.</p>
        <label v-for="kind in actionChoices" :key="kind" class="mcp-session-action">
          <input type="checkbox" :checked="selected === kind" :disabled="busy" @change="setSelected(kind, $event)" />
          {{ actionLabels[kind] }}
        </label>
      </template>
      <div class="gomar-feature-actions">
        <button v-if="!credential" type="button" class="secondary" :disabled="busy || !selected" @click="issue">Crear sesión de propuestas</button>
        <template v-if="available">
          <button type="button" class="secondary" :disabled="busy" @click="copyKey">Copiar clave para propuestas</button>
          <button type="button" class="secondary" :disabled="busy" @click="showKey">Mostrar clave una vez</button>
        </template>
        <button v-if="revealed" type="button" class="secondary" @click="hideKey">Ocultar clave</button>
      </div>
      <p v-if="revealed" class="mcp-connect-secret"><code>{{ revealed }}</code></p>
      <p>Guardá la clave en un archivo privado de tu equipo (permiso 0600). No la pegues en chats, URLs ni comandos.</p>
      <p>Configurá <code>gomar-mcp</code> con el archivo de lectura habitual y <code>--proposal-session-token-file</code> apuntando a este otro archivo privado.</p>
      <p v-if="setupAction">Para esta acción, agregá <code>--proposal-session-action={{ setupAction }}</code>.</p>
    </details>
    <h3>Sesiones activas de {{ outletName }}</h3>
    <p v-if="!sessions.length && !busy && !error">No hay sesiones activas para este local.</p>
    <ul v-else-if="sessions.length" class="gomar-feature-list">
      <li v-for="session in sessions" :key="session.id" class="gomar-feature-article">
        <span>Sesión {{ session.id }} · {{ session.actions.map(actionText).join(', ') }} · Vence a las {{ displayExpiry(Date.parse(session.expires_at)) }}</span>
        <button type="button" class="danger" :disabled="busy" @click="revoke(session.id)">Revocar sesión {{ session.id }}</button>
      </li>
    </ul>
    <div class="gomar-feature-actions">
      <button type="button" class="secondary" :disabled="busy" @click="refresh">Actualizar sesiones y propuestas</button>
    </div>
    <p v-if="actionChoices.length && !items.length && !busy && !error">No hay propuestas para este local.</p>
    <ul v-else-if="actionChoices.length && items.length" class="gomar-feature-list">
      <li v-for="item in items" :key="`${item.kind}-${item.id}`" class="gomar-feature-article">
        <span>Propuesta {{ item.id }} · {{ actionLabels[item.kind] }} · {{ labels[item.status] }}</span>
        <button type="button" class="text-button" :disabled="busy" @click="preview(item.id, item.kind)">Ver propuesta {{ item.id }}{{ actionUI[item.kind].previewSuffix }}</button>
      </li>
    </ul>
    <section v-if="viewed && allowed(viewed.kind)" class="card subsection gomar-feature" aria-label="Vista previa de la propuesta">
      <h3>Vista previa</h3>
      <template v-if="viewed.status === 'pending'">
        <div v-if="viewed.kind === cancelAction && viewed.cancellation">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p>Referencia: <strong>{{ viewed.cancellation.reservation_reference }}</strong></p>
          <p>Horario de Buenos Aires: <strong>{{ viewed.cancellation.start_local.slice(0, 10) }} · {{ viewed.cancellation.start_local.slice(11, 16) }}</strong></p>
          <p>Personas: <strong>{{ viewed.cancellation.party_size }}</strong> · Estado actual: {{ viewed.cancellation.status === 'confirmed' ? 'Confirmada' : 'Pendiente' }}.</p>
          <p v-if="viewed.cancellation.table_label">Mesa asignada: <strong>{{ viewed.cancellation.table_label }}</strong>. La mesa quedará libre.</p>
          <p v-else>No hay mesa asignada.</p>
          <p>La cancelación no se puede deshacer y no envía aviso.</p>
        </div>
        <div v-else-if="viewed.kind === tableAddAction && viewed.table_add">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p>Mesa: <strong>{{ viewed.table_add.table_label }}</strong> · Capacidad: <strong>{{ viewed.table_add.capacity }} personas</strong>.</p>
          <p>Las reservas de este local están desactivadas y seguirán así. La mesa puede ampliar la disponibilidad de horarios ya creados solo si alguien habilita las reservas más adelante.</p>
          <p>Por ahora no se puede retirar esta mesa desde Gomar. No se agregan turnos, no se modifican solicitudes y no se envían avisos.</p>
        </div>
        <div v-else-if="viewed.kind === reservationAction && viewed.confirmation">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p>Referencia: <strong>{{ viewed.confirmation.reservation_reference }}</strong></p>
          <p>Horario de Buenos Aires: <strong>{{ viewed.confirmation.start_local.slice(0, 10) }} · {{ viewed.confirmation.start_local.slice(11, 16) }}</strong></p>
          <p>Personas: <strong>{{ viewed.confirmation.party_size }}</strong> · Estado actual: Pendiente.</p>
          <p>Mesa: <strong>{{ viewed.confirmation.table_label }}</strong> · Capacidad: <strong>{{ viewed.confirmation.table_capacity }}</strong>.</p>
          <p>Solo se confirma esta reserva. No se envían mensajes.</p>
        </div>
        <div v-else-if="viewed.kind === createAction && viewed.product">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p class="gomar-feature-text">Producto nuevo: <strong>{{ viewed.product.name }}</strong></p>
          <p>Precio: <strong>ARS {{ amount(viewed.product.price_minor) }}</strong></p>
          <p>Disponibilidad: <strong>{{ stateLabel(viewed.product.active) }}</strong></p>
          <p>Productos en el catálogo: {{ viewed.product.catalog_count }} de {{ viewed.product.catalog_limit }}.</p>
          <p>No se modifican el stock ni las ventas ya registradas.</p>
        </div>
        <div v-else-if="isCatalog(viewed.kind) && viewed.change">
          <p>Local: <strong>{{ outletName }}</strong> · Producto {{ viewed.change.product_id }} · Versión {{ viewed.change.expected_revision }}</p>
          <p class="gomar-feature-text">Nombre sin cambios: <strong>{{ viewed.change.name }}</strong></p>
          <template v-if="viewed.kind === priceAction">
            <p>Precio actual: <strong>ARS {{ amount(viewed.change.current_price_minor) }}</strong></p>
            <p>Precio propuesto: <strong>ARS {{ amount(viewed.change.price_minor) }}</strong></p>
            <p>Disponibilidad sin cambios: {{ stateLabel(viewed.change.active) }}.</p>
          </template>
          <template v-else>
            <p>Disponibilidad actual: <strong>{{ stateLabel(viewed.change.current_active) }}</strong></p>
            <p>Disponibilidad propuesta: <strong>{{ stateLabel(viewed.change.active) }}</strong></p>
            <p>Precio sin cambios: ARS {{ amount(viewed.change.price_minor) }}.</p>
          </template>
          <p>No se modifican el stock ni las ventas ya registradas.</p>
        </div>
        <div v-else-if="viewed.kind === supplierAction && viewed.supplier">
          <p>Empresa: <strong>{{ businessName }}</strong></p>
          <p>Local que autoriza el acceso: <strong>{{ outletName }}</strong></p>
          <p><strong>El cambio afecta a todos los locales de esta empresa.</strong></p>
          <p>Proveedor {{ viewed.supplier.supplier_id }} · Versión {{ viewed.supplier.expected_revision }}</p>
          <p>Razón social actual:</p>
          <pre class="gomar-feature-text proposal-body" aria-label="Razón social actual">{{ viewed.supplier.current_name }}</pre>
          <p v-if="!viewed.supplier.current_name">(Vacía)</p>
          <p>Razón social propuesta:</p>
          <pre class="gomar-feature-text proposal-body" aria-label="Razón social propuesta">{{ viewed.supplier.name }}</pre>
          <p>Solo cambia la razón social. No se modifican datos fiscales, contactos, documentos ni pagos.</p>
        </div>
        <div v-else-if="viewed.kind === stockSetupAction && viewed.stock_setup">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p class="gomar-feature-text">Producto: <strong>{{ viewed.stock_setup.name }}</strong> ({{ viewed.stock_setup.product_id }}) · Versión {{ viewed.stock_setup.expected_revision }}</p>
          <p>Mínimo inicial para avisos: <strong>{{ stockUnits.format(viewed.stock_setup.minimum) }} unidades</strong>.</p>
          <p>Se guarda la configuración. El seguimiento queda <strong>desactivado</strong> y los avisos todavía no funcionan.</p>
          <p>No se cargan existencias, movimientos ni ventas. Activar el seguimiento requiere otra aprobación.</p>
        </div>
        <div v-else-if="viewed.kind === trackingAction && viewed.tracking_change">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p class="gomar-feature-text">Producto: <strong>{{ viewed.tracking_change.name }}</strong> ({{ viewed.tracking_change.product_id }}) · Versión {{ viewed.tracking_change.expected_revision }}</p>
          <p>Seguimiento: <strong>{{ trackingLabel(viewed.tracking_change.current_tracking_enabled) }} → {{ trackingLabel(viewed.tracking_change.tracking_enabled) }}</strong></p>
          <p>Mínimo sin cambios: <strong>{{ stockUnits.format(viewed.tracking_change.minimum) }} unidades</strong>.</p>
          <p>No se modifica el saldo estimado, el historial ni las ventas ya registradas.</p>
          <p v-if="!viewed.tracking_change.tracking_enabled">Las ventas que se registren mientras el seguimiento esté desactivado, incluidas las que se sincronicen después de vender sin conexión, no descuentan stock. Al reactivarlo, esos descuentos no se recuperan.</p>
          <p v-else>Se vuelve a descontar stock desde el saldo estimado anterior. Esto no deshace la pausa ni descuenta ventas registradas durante la pausa.</p>
        </div>
        <div v-else-if="viewed.kind === minimumAction && viewed.minimum_change">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p class="gomar-feature-text">Producto: <strong>{{ viewed.minimum_change.name }}</strong> ({{ viewed.minimum_change.product_id }}) · Versión {{ viewed.minimum_change.expected_revision }}</p>
          <p>Mínimo actual: <strong>{{ stockUnits.format(viewed.minimum_change.current_minimum) }} unidades</strong></p>
          <p>Mínimo propuesto: <strong>{{ stockUnits.format(viewed.minimum_change.minimum) }} unidades</strong></p>
          <p>Solo cambia el mínimo. No se modifica el saldo, el seguimiento de stock ni la versión del catálogo.</p>
        </div>
        <div v-else-if="isStock(viewed.kind) && viewed.adjustment">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p class="gomar-feature-text">Producto: <strong>{{ viewed.adjustment.product_name }}</strong> ({{ viewed.adjustment.product_id }}) · Versión {{ viewed.adjustment.revision }}</p>
          <p>{{ viewed.kind === wasteAction ? 'Unidades de merma' : 'Unidades contadas' }}: <strong>{{ stockUnits.format(viewed.adjustment.quantity) }}</strong></p>
          <p>Saldo: <strong>{{ stockUnits.format(viewed.adjustment.before) }} → {{ stockUnits.format(viewed.adjustment.after) }}</strong></p>
          <p>Cambio: <strong>{{ stockDelta.format(viewed.adjustment.delta) }}</strong></p>
          <p>Si hubo otro movimiento desde la propuesta, no se aplica este ajuste. No se cambian precios ni se registra una venta.</p>
        </div>
        <div v-else-if="viewed.kind === editAction && viewed.edit">
          <p>Local: <strong>{{ outletName }}</strong> · Comprobante {{ viewed.edit.document_id }} · Versión {{ viewed.edit.expected_revision }}</p>
          <p>Moneda sin cambios: <strong>{{ viewed.edit.currency }}</strong></p>
          <section
            v-for="side in (['before', 'after'] as const)" :key="side"
            :aria-label="side === 'before' ? 'Antes de la edición' : 'Después de la edición'"
          >
            <h4>{{ side === 'before' ? 'Antes' : 'Después' }}</h4>
            <dl class="document-edit-values">
              <dt>Emisión</dt><dd>{{ viewed.edit[side].issue_date }}</dd>
              <dt>Vencimiento</dt><dd>{{ viewed.edit[side].due_date ?? 'No informado' }}</dd>
              <dt>Neto</dt><dd>{{ amount(viewed.edit[side].net_minor) }}</dd>
              <dt>Impuestos</dt><dd>{{ amount(viewed.edit[side].tax_minor) }}</dd>
              <dt>Total</dt><dd>{{ viewed.edit.currency }} {{ amount(viewed.edit[side].total_minor) }}</dd>
            </dl>
          </section>
          <p>Motivo:</p>
          <pre class="gomar-feature-text proposal-body" aria-label="Motivo de la edición">{{ viewed.edit.reason }}</pre>
          <p>Solo se guardan fechas e importes. El comprobante sigue en borrador. No se cambian archivos, no se elimina el comprobante ni se registra una deuda o un pago.</p>
        </div>
        <div v-else-if="viewed.kind === draftAction && viewed.draft">
          <p>Local: <strong>{{ outletName }}</strong></p>
          <p>Usar la referencia para buscar coincidencias: <strong>{{ viewed.draft.match_reference ? 'Sí' : 'No' }}</strong></p>
          <p>Solo se crea un borrador vacío. No se importan archivos ni movimientos y no se cierra una conciliación.</p>
        </div>
        <div v-else-if="viewed.kind === reviewAction && viewed.review">
          <p>Local: <strong>{{ outletName }}</strong> · Comprobante {{ viewed.review.document_id }} · Versión {{ viewed.review.expected_revision }}</p>
          <p>Tipo: {{ documentTypes[viewed.review.type] }} · Total: {{ viewed.review.currency }} {{ prices.format(viewed.review.total_minor / 100) }}</p>
          <p>Motivo:</p>
          <pre class="gomar-feature-text proposal-body" aria-label="Motivo de la revisión">{{ viewed.review.reason }}</pre>
          <p>Solo se marca como revisado. No se registra una deuda ni un pago.</p>
        </div>
        <div v-else-if="viewed.rename">
          <p>Local: <strong>{{ outletName }}</strong> · Producto {{ viewed.rename.product_id }} · Versión {{ viewed.rename.expected_revision }}</p>
          <p>Nombre actual:</p>
          <pre class="gomar-feature-text proposal-body" aria-label="Nombre actual">{{ viewed.rename.current_name }}</pre>
          <p>Nombre propuesto:</p>
          <pre class="gomar-feature-text proposal-body" aria-label="Nombre propuesto">{{ viewed.rename.name }}</pre>
          <p>Precio sin cambios: ARS {{ prices.format(viewed.rename.unchanged_price_minor / 100) }}</p>
          <p>Estado sin cambios: {{ viewed.rename.unchanged_active ? 'Activo' : 'Inactivo' }}.</p>
          <p>Solo se cambia el nombre. No se modifican el stock ni las ventas.</p>
        </div>
        <div v-if="!deciding" class="gomar-feature-actions">
          <button type="button" class="primary" :disabled="busy || !canDecide" @click="begin('approve')">{{ actionUI[viewed.kind].proceed }}</button>
          <button type="button" class="secondary" :disabled="busy || !canDecide" @click="begin('reject')">Rechazar propuesta</button>
          <button type="button" class="text-button" :disabled="busy" @click="clearPreview">Cerrar vista previa</button>
        </div>
        <div v-else class="gomar-feature-actions" role="group" aria-label="Confirmar decisión">
          <p>{{ deciding.action === 'approve' ? actionUI[viewed.kind].confirm : actionUI[viewed.kind].rejectConfirm }}</p>
          <label v-if="deciding.kind === cancelAction && deciding.action === 'approve'">
            <input v-model="cancelAcknowledged" type="checkbox" :disabled="busy">
            Entiendo que la cancelación es definitiva.
          </label>
          <label v-if="needsTableAddAcknowledgement">
            <input v-model="tableAddAcknowledged" type="checkbox" :disabled="busy">
            Entiendo que Gomar no permite retirar esta mesa y que podría ampliar la disponibilidad si se habilitan las reservas.
          </label>
          <label v-if="needsTrackingAcknowledgement">
            <input v-model="trackingAcknowledged" type="checkbox" :disabled="busy">
            Entiendo que las ventas registradas durante la pausa no descuentan stock, incluso al sincronizar ventas sin conexión, y que esos descuentos no se recuperan al reactivar.
          </label>
          <button
            type="button" :class="deciding.action === 'approve' && deciding.kind !== cancelAction ? 'primary' : 'danger'"
            :disabled="busy || (deciding.kind === cancelAction && deciding.action === 'approve' && !cancelAcknowledged) || (needsTrackingAcknowledgement && !trackingAcknowledged) || (needsTableAddAcknowledgement && !tableAddAcknowledged)"
            @click="decide"
          >
            {{ deciding.action === 'approve' ? actionUI[viewed.kind].approve : 'Sí, rechazar' }}
          </button>
          <button type="button" class="secondary" :disabled="busy" @click="deciding = null">Volver sin cambios</button>
        </div>
      </template>
      <template v-else>
        <p>{{ labels[viewed.status] }}</p>
        <p v-if="viewed.kind === supplierAction && viewed.status === 'approved' && viewed.supplier_id && viewed.supplier_revision">Proveedor {{ viewed.supplier_id }} · Versión {{ viewed.supplier_revision }}</p>
        <p v-if="viewed.kind === minimumAction && viewed.status === 'approved' && viewed.product_id && validMinimum(viewed.minimum)">Producto {{ viewed.product_id }} · Mínimo: {{ stockUnits.format(viewed.minimum) }} unidades</p>
        <p v-if="viewed.kind === trackingAction && viewed.status === 'approved' && viewed.product_id && typeof viewed.tracking_enabled === 'boolean'">Producto {{ viewed.product_id }} · Seguimiento: {{ trackingLabel(viewed.tracking_enabled) }}</p>
        <p v-if="viewed.kind === stockSetupAction && viewed.status === 'approved' && viewed.product_id && validMinimum(viewed.minimum) && viewed.tracking_enabled === false">Producto {{ viewed.product_id }} · Mínimo: {{ stockUnits.format(viewed.minimum) }} unidades · Seguimiento desactivado al crear.</p>
        <p v-if="viewed.kind !== stockSetupAction && viewed.kind !== trackingAction && viewed.kind !== minimumAction && viewed.status === 'approved' && viewed.product_id">Producto {{ viewed.product_id }} · Versión {{ viewed.product_revision }}</p>
        <p v-if="viewed.status === 'approved' && viewed.document_id">Comprobante {{ viewed.document_id }} · Versión {{ viewed.document_revision }}</p>
        <p v-if="viewed.status === 'approved' && viewed.run_id">Conciliación {{ viewed.run_id }}</p>
        <p v-if="viewed.status === 'approved' && viewed.movement_id">Movimiento {{ viewed.movement_id }}</p>
        <p v-if="viewed.kind === tableAddAction && viewed.status === 'approved' && viewed.table_label && viewed.capacity">{{ viewed.table_label }} · Capacidad: {{ viewed.capacity }} personas · Reservas desactivadas al agregar.</p>
        <p v-if="isReservation(viewed.kind) && viewed.status === 'approved' && reservationReference(viewed.reservation_reference)">Reserva {{ viewed.reservation_reference }}</p>
        <button type="button" class="text-button" @click="clearPreview">Cerrar vista previa</button>
      </template>
    </section>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.mcp-session-action { display: flex; align-items: baseline; gap: .5rem; }
.mcp-session-action input { width: auto; }
.document-edit-values { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 2fr); gap: .25rem .75rem; }
.document-edit-values dt { font-weight: 600; }
.document-edit-values dd { margin: 0; overflow-wrap: anywhere; }
</style>
