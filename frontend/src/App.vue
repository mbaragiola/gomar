<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { lockOfflinePOS } from './pos/storage'
import { ApiError, download, request } from './api'
import ThemeSelect from './components/ThemeSelect.vue'
import McpDocumentProposals from './components/McpDocumentProposals.vue'
import McpProposalSessions from './components/McpProposalSessions.vue'
import McpStockReceiptProposals from './components/McpStockReceiptProposals.vue'
import McpWikiEditProposals from './components/McpWikiEditProposals.vue'
import McpWikiProposals from './components/McpWikiProposals.vue'
import McpConnect from './components/McpConnect.vue'
import InstallApp from './components/InstallApp.vue'
import StockPanel from './components/StockPanel.vue'
import DeliveryInbox from './components/DeliveryInbox.vue'
import KnowledgePanel from './components/KnowledgePanel.vue'
import PublicReservations from './components/PublicReservations.vue'
import StaffReservations from './components/StaffReservations.vue'
import ReconciliationPanel from './components/ReconciliationPanel.vue'
import { localDate, localDateTime, money, todayInBuenosAires } from './format'
import type { Document, Entity, EntityManagement, IntakeOperation, OcrConfig, OcrDraft, Outlet, Payable, Payment, Supplier } from './types'

const publicVenue = /^\/reservas\/([a-z0-9][a-z0-9-]{0,63})\/?$/.exec(window.location.pathname)?.[1]
const authenticated = ref(false)
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const username = ref('')
const entities = ref<Entity[]>([])
const outlets = ref<Outlet[]>([])
const suppliers = ref<Supplier[]>([])
const documents = ref<Document[]>([])
const total = ref<{ cantidad: number; totales: { moneda: string; cantidad: number; total: string }[] }>({ cantidad: 0, totales: [] })
const selectedEntityId = ref<number | null>(null)
const selectedOutletId = ref<number | null>(null)
const selectedDocument = ref<Document | null>(null)
const payables = ref<Payable[]>([])
const payments = ref<Payment[]>([])
const paymentDraft = ref<Payment | null>(null)
const showAccountsPayable = ref(false)
const showReconciliation = ref(false)
const paymentReceipt = ref<File | null>(null)
type StaffView = 'documents' | 'payables' | 'reconciliation' | 'stock' | 'delivery' | 'reservations' | 'knowledge' | 'chat' | 'suppliers' | 'access'
const staffView = ref<StaffView>('documents')
const staffHeading = ref<HTMLHeadingElement | null>(null)
const documentHeading = ref<HTMLHeadingElement | null>(null)
const mobileDetail = ref(false)
const showCreate = ref(false)
const showSupplier = ref(false)
const showSupplierManagement = ref(false)
const showManagement = ref(false)
const showKnowledge = ref(false)
const showDelivery = ref(false)
const showStock = ref(false)
const showReservations = ref(false)
const management = ref<EntityManagement | null>(null)
const uploadFile = ref<File | null>(null)
const intakeFile = ref<File | null>(null)
const intakeOperation = ref<IntakeOperation | null>(null)
const ocrDraft = ref<OcrDraft | null>(null)
const ocrConfig = ref<OcrConfig>({
  habilitado: false,
  proveedor: 'Gemini API',
  version_aviso: '',
  aviso: '',
  mensaje: 'OCR no está habilitado, aprobado o configurado. Cargá los datos a mano y después adjuntá el archivo.',
})
const ocrDisclosureAcceptedVersion = ref<string | null>(null)
const createSupplierOnConfirm = ref(false)
let sessionGeneration = 0
let contextGeneration = 0
let listGeneration = 0
let detailGeneration = 0
let paymentDetailGeneration = 0
let busyGeneration = 0

const loginForm = reactive({ usuario: '', clave: '' })
const supplierForm = reactive({ razon_social: '', cuit: '' })
const newOutletForm = reactive({ nombre: '', activa: true })
const newMembershipForm = reactive({ usuario: '', rol: 'unassigned', activa: true, outlet_ids: [] as number[] })
const documentForm = reactive({
  supplier_id: 0,
  issuer_cuit: '',
  tipo: 'invoice',
  letra: '',
  punto_venta: 1,
  numero: 1,
  fecha_emision: todayInBuenosAires(),
  fecha_vencimiento: '',
  moneda: 'ARS',
  neto: '',
  iva: '',
  total: '',
  descripcion: '',
})

const currentEntity = computed(() => entities.value.find((item) => item.id === selectedEntityId.value) ?? null)
const hasCapability = (name: string) => currentEntity.value?.capacidades?.includes(name) ?? false
const canManageDrafts = computed(() => hasCapability('manage_draft_documents'))
const canReview = computed(() => hasCapability('review_documents'))
const canDownload = computed(() => hasCapability('download_attachments'))
const canManageSuppliers = computed(() => hasCapability('manage_suppliers'))
const canReadStock = computed(() => hasCapability('read_pos_stock'))
const canReadDelivery = computed(() => hasCapability('read_delivery_orders'))
const canReviewDocumentProposals = computed(() => hasCapability('mcp_document_proposals'))
const canRenameViaMcp = computed(() => hasCapability('mcp_pos_rename_proposals'))
const canReviewDocViaMcp = computed(() => hasCapability('mcp_document_review_proposals'))
const canCreateReconciliationViaMcp = computed(() => hasCapability('mcp_reconciliation_draft_proposals'))
const canEditDocViaMcp = computed(() => hasCapability('mcp_document_edit_proposals'))
const canAdjustStockViaMcp = computed(() => hasCapability('mcp_stock_adjustment_proposals'))
const canChangeCatalogViaMcp = computed(() => hasCapability('mcp_pos_catalog_proposals'))
const canConfirmReservationsViaMcp = computed(() => hasCapability('mcp_reservation_confirm_proposals'))
const canManageMcpProposalSessions = computed(() => hasCapability('mcp_proposal_session_management') || canRenameViaMcp.value || canReviewDocViaMcp.value || canCreateReconciliationViaMcp.value || canEditDocViaMcp.value || canAdjustStockViaMcp.value || canChangeCatalogViaMcp.value || canConfirmReservationsViaMcp.value)
const canReviewStockReceiptProposals = computed(() => hasCapability('mcp_stock_receipt_proposals'))
const canReviewWikiEditProposals = computed(() => hasCapability('mcp_wiki_edit_proposals'))
const canReviewWikiProposals = computed(() => hasCapability('mcp_wiki_proposals'))
const canConnectMCP = computed(() => hasCapability('mcp_read'))
const canReadBusiness = computed(() => hasCapability('read_business_data'))
const canManageAccess = computed(() => hasCapability('manage_entity_access'))
const canRecordPayments = computed(() => hasCapability('record_supplier_payments'))
const canReversePayments = computed(() => hasCapability('reverse_supplier_payments'))
const canReconcile = computed(() => hasCapability('reconcile_financial_data'))
const showDocumentDetail = computed(() => mobileDetail.value && !!selectedDocument.value)
const hasOutlet = computed(() => !!selectedOutletId.value && outlets.value.some(item => item.id === selectedOutletId.value))
const canUseChat = computed(() => hasOutlet.value && (canConnectMCP.value || canReviewWikiProposals.value ||
  canReviewWikiEditProposals.value || canReviewDocumentProposals.value || canReviewStockReceiptProposals.value || canManageMcpProposalSessions.value))
const viewNames: Record<StaffView, string> = {
  documents: 'Comprobantes', payables: 'Cuentas a pagar', reconciliation: 'Conciliación financiera',
  stock: 'Stock del local', delivery: 'Bandeja de delivery', reservations: 'Reservas del local', knowledge: 'Manual del local',
  chat: 'Chat y propuestas', suppliers: 'Proveedores', access: 'Accesos y locales',
}
function focusStaffHeading() {
  void nextTick(() => staffHeading.value?.focus())
}
function selectStaffView(view: StaffView) {
  if (staffView.value === view) return
  staffView.value = view
  mobileDetail.value = false
  if (view === 'access' && !showManagement.value) void toggleManagement()
  if (view !== 'access') showManagement.value = false
  if (view === 'payables' && !showAccountsPayable.value) void toggleAccountsPayable()
  if (view !== 'payables') showAccountsPayable.value = false
  showReconciliation.value = view === 'reconciliation'
  showStock.value = view === 'stock'
  showDelivery.value = view === 'delivery'
  showReservations.value = view === 'reservations'
  showKnowledge.value = view === 'knowledge'
  showSupplierManagement.value = view === 'suppliers'
  focusStaffHeading()
}
function showMobileDocument() {
  if (!window.matchMedia?.('(max-width: 850px)').matches || staffView.value !== 'documents' || !selectedDocument.value) return
  mobileDetail.value = true
  void nextTick(() => documentHeading.value?.focus())
}
function returnToDocuments() {
  mobileDetail.value = false
  focusStaffHeading()
}
const canManageSelectedDocument = computed(() => selectedDocument.value?.estado === 'reviewed'
  ? hasCapability('manage_reviewed_documents')
  : canManageDrafts.value)
const paymentForm = reactive({ supplier_id: 0, moneda: 'ARS', total: '', metodo: 'bank_transfer', referencia: '' })
const allocationAmounts = reactive<Record<number, string>>({})
const eligiblePayables = computed(() => payables.value.filter((item) => (
  item.supplier.id === paymentForm.supplier_id && item.moneda === paymentForm.moneda && item.estado !== 'settled'
)))
const allocatedTotal = computed(() => eligiblePayables.value.reduce((sum, item) => {
  const parsed = Number((allocationAmounts[item.id] || '0').replace(',', '.'))
  return sum + (Number.isFinite(parsed) ? parsed : 0)
}, 0))

function resetForms() {
  Object.assign(supplierForm, { razon_social: '', cuit: '' })
  Object.assign(documentForm, {
    supplier_id: 0,
    issuer_cuit: '',
    tipo: 'invoice',
    letra: '',
    punto_venta: 1,
    numero: 1,
    fecha_emision: todayInBuenosAires(),
    fecha_vencimiento: '',
    moneda: 'ARS',
    neto: '',
    iva: '',
    total: '',
    descripcion: '',
  })
}

function startBusy(): number {
  busyGeneration += 1
  busy.value = true
  return busyGeneration
}

function clearBusy() {
  busyGeneration += 1
  busy.value = false
}

function finishBusy(token: number) {
  if (token === busyGeneration) busy.value = false
}

function clearDocumentState(resetBusy = true) {
  if (resetBusy) clearBusy()
  documents.value = []
  total.value = { cantidad: 0, totales: [] }
  selectedDocument.value = null
  showCreate.value = false
  showSupplier.value = false
  showSupplierManagement.value = false
  uploadFile.value = null
  intakeFile.value = null
  intakeOperation.value = null
  ocrDraft.value = null
  ocrDisclosureAcceptedVersion.value = null
  createSupplierOnConfirm.value = false
  detailGeneration += 1
  resetForms()
}

function clearContextState(resetBusy = true) {
  staffView.value = 'documents'
  mobileDetail.value = false
  outlets.value = []
  suppliers.value = []
  selectedOutletId.value = null
  showManagement.value = false
  showKnowledge.value = false
  showDelivery.value = false
  showStock.value = false
  showReservations.value = false
  management.value = null
  payables.value = []
  payments.value = []
  paymentDraft.value = null
  paymentDetailGeneration += 1
  showAccountsPayable.value = false
  showReconciliation.value = false
  paymentReceipt.value = null
  Object.keys(allocationAmounts).forEach((key) => delete allocationAmounts[Number(key)])
  Object.assign(paymentForm, { supplier_id: 0, moneda: 'ARS', total: '', metodo: 'bank_transfer', referencia: '' })
  Object.assign(newOutletForm, { nombre: '', activa: true })
  Object.assign(newMembershipForm, { usuario: '', rol: 'unassigned', activa: true, outlet_ids: [] })
  clearDocumentState(resetBusy)
}

function resetUserState(): number {
  sessionGeneration += 1
  contextGeneration += 1
  listGeneration += 1
  authenticated.value = false
  error.value = ''
  username.value = ''
  entities.value = []
  selectedEntityId.value = null
  clearContextState()
  Object.assign(loginForm, { usuario: '', clave: '' })
  return sessionGeneration
}

function sessionIsCurrent(sessionToken: number) {
  return sessionToken === sessionGeneration
}

function contextIsCurrent(sessionToken: number, contextToken: number, entityId: number) {
  return sessionIsCurrent(sessionToken) && contextToken === contextGeneration && selectedEntityId.value === entityId
}

function showError(value: unknown) {
  error.value = value instanceof ApiError ? value.message : 'Ocurrió un error inesperado.'
}

async function initialize() {
  const sessionToken = resetUserState()
  loading.value = true
  try {
    await request('/auth/csrf')
    const me = await request<{ usuario: string }>('/auth/me')
    if (!sessionIsCurrent(sessionToken)) return
    username.value = me.usuario
    await loadEntities(sessionToken)
    if (sessionIsCurrent(sessionToken)) authenticated.value = true
  } catch (value) {
    if (sessionIsCurrent(sessionToken) && (!(value instanceof ApiError) || value.status !== 401)) showError(value)
  } finally {
    if (sessionIsCurrent(sessionToken)) loading.value = false
  }
}

async function signIn() {
  const credentials = { ...loginForm }
  const sessionToken = resetUserState()
  const busyToken = startBusy()
  try {
    const result = await request<{ usuario: string }>('/auth/login', { method: 'POST', body: JSON.stringify(credentials) })
    if (!sessionIsCurrent(sessionToken)) return
    lockOfflinePOS()
    username.value = result.usuario
    await loadEntities(sessionToken)
    if (sessionIsCurrent(sessionToken)) authenticated.value = true
  } catch (value) {
    if (sessionIsCurrent(sessionToken)) {
      resetUserState()
      showError(value)
    }
  } finally {
    finishBusy(busyToken)
  }
}

async function signOut() {
  lockOfflinePOS()
  const sessionToken = resetUserState()
  try {
    await request('/auth/logout', { method: 'POST' })
  } catch (value) {
    if (sessionIsCurrent(sessionToken)) showError(value)
  }
}

async function loadEntities(sessionToken: number) {
  const values = await request<Entity[]>('/entities')
  if (!sessionIsCurrent(sessionToken)) return
  entities.value = values
  selectedEntityId.value = entities.value[0]?.id ?? null
  if (selectedEntityId.value) await loadContext(sessionToken, false)
}

async function loadContext(sessionToken = sessionGeneration, resetBusy = true) {
  const contextToken = ++contextGeneration
  clearContextState(resetBusy)
  error.value = ''
  const entityId = selectedEntityId.value
  if (!entityId) return
  const nextOutlets = await request<Outlet[]>(`/entities/${entityId}/outlets`)
  const nextSuppliers = nextOutlets.length
    ? await request<Supplier[]>(`/entities/${entityId}/suppliers`)
    : []
  if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
  outlets.value = nextOutlets
  suppliers.value = nextSuppliers
  let nextOcrConfig: OcrConfig
  try {
    nextOcrConfig = await request<OcrConfig>('/ocr/config')
  } catch {
    nextOcrConfig = {
      habilitado: false,
      proveedor: 'Gemini API',
      version_aviso: '',
      aviso: '',
      mensaje: 'OCR no está habilitado, aprobado o configurado. Cargá los datos a mano y después adjuntá el archivo.',
    }
  }
  if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
  if (nextOcrConfig.version_aviso !== ocrConfig.value.version_aviso) {
    ocrDisclosureAcceptedVersion.value = null
  }
  ocrConfig.value = nextOcrConfig
  selectedOutletId.value = outlets.value[0]?.id ?? null
  documentForm.supplier_id = suppliers.value[0]?.id ?? 0
  documentForm.issuer_cuit = suppliers.value[0]?.cuit ?? ''
  await loadDocuments(sessionToken, contextToken)
}

async function loadManagement(sessionToken = sessionGeneration, contextToken = contextGeneration) {
  if (!selectedEntityId.value) return
  const entityId = selectedEntityId.value
  try {
    const value = await request<EntityManagement>(`/entities/${entityId}/management`)
    if (contextIsCurrent(sessionToken, contextToken, entityId)) management.value = value
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function toggleManagement() {
  showManagement.value = !showManagement.value
  if (showManagement.value) await loadManagement()
}

async function saveEntity() {
  if (!selectedEntityId.value || !management.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  try {
    const value = await request<{ nombre: string; cuit: string }>(`/entities/${entityId}`, {
      method: 'PUT', body: JSON.stringify(management.value.entidad),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
    const entity = entities.value.find((item) => item.id === entityId)
    if (entity) Object.assign(entity, value)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function createOutlet() {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  try {
    await request(`/entities/${entityId}/outlets`, { method: 'POST', body: JSON.stringify(newOutletForm) })
    if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
    Object.assign(newOutletForm, { nombre: '', activa: true })
    await loadManagement(sessionToken, contextToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function saveOutlet(outlet: EntityManagement['locales'][number]) {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  try {
    await request(`/entities/${entityId}/outlets/${outlet.id}`, {
      method: 'PUT', body: JSON.stringify({ nombre: outlet.nombre, activa: outlet.activa }),
    })
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
    return
  }
  if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
  const reloadContextToken = contextGeneration + 1
  try {
    await loadContext(sessionToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, reloadContextToken, entityId)) showError(value)
  }
}

async function createMembership() {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  try {
    await request(`/entities/${entityId}/memberships`, {
      method: 'POST', body: JSON.stringify(newMembershipForm),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
    Object.assign(newMembershipForm, { usuario: '', rol: 'unassigned', activa: true, outlet_ids: [] })
    await loadManagement(sessionToken, contextToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function saveMembership(item: EntityManagement['membresias'][number]) {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  try {
    await request(`/entities/${entityId}/memberships/${item.id}`, {
      method: 'PUT', body: JSON.stringify({ rol: item.rol, activa: item.activa, outlet_ids: item.outlet_ids }),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
    await loadManagement(sessionToken, contextToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function saveSupplier(supplier: Supplier) {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  try {
    await request(`/entities/${entityId}/suppliers/${supplier.id}`, {
      method: 'PUT', body: JSON.stringify(supplier),
    })
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
    return
  }
  if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
  const reloadContextToken = contextGeneration + 1
  try {
    await loadContext(sessionToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, reloadContextToken, entityId)) showError(value)
  }
}

async function loadDocuments(sessionToken = sessionGeneration, contextToken = contextGeneration) {
  const listToken = ++listGeneration
  documents.value = []
  total.value = { cantidad: 0, totales: [] }
  selectedDocument.value = null
  const entityId = selectedEntityId.value
  if (!entityId) return
  const outletId = selectedOutletId.value
  const query = outletId ? `?outlet_id=${outletId}` : ''
  const [nextDocuments, nextTotal] = await Promise.all([
    request<Document[]>(`/entities/${entityId}/documents${query}`),
    request<{ cantidad: number; totales: { moneda: string; cantidad: number; total: string }[] }>(`/entities/${entityId}/totals${query}`),
  ])
  if (!contextIsCurrent(sessionToken, contextToken, entityId) || listToken !== listGeneration || outletId !== selectedOutletId.value) return
  documents.value = nextDocuments
  total.value = nextTotal
}

async function changeEntity() {
  const sessionToken = sessionGeneration
  try {
    await loadContext(sessionToken)
  } catch (value) {
    if (sessionIsCurrent(sessionToken)) showError(value)
  }
}

async function changeOutlet() {
  selectStaffView('documents')
  const contextToken = ++contextGeneration
  clearDocumentState()
  documentForm.supplier_id = suppliers.value[0]?.id ?? 0
  documentForm.issuer_cuit = suppliers.value[0]?.cuit ?? ''
  error.value = ''
  try {
    await loadDocuments(sessionGeneration, contextToken)
  } catch (value) {
    if (contextToken === contextGeneration) showError(value)
  }
}

async function openDocument(id: number) {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const detailToken = ++detailGeneration
  selectedDocument.value = null
  error.value = ''
  try {
    const detail = await request<Document>(`/entities/${entityId}/documents/${id}`)
    if (contextIsCurrent(sessionToken, contextToken, entityId) && detailToken === detailGeneration) {
      selectedDocument.value = detail
      showMobileDocument()
    }
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId) && detailToken === detailGeneration) showError(value)
  }
}

async function createSupplier() {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const busyToken = startBusy()
  error.value = ''
  try {
    const supplier = await request<Supplier>(`/entities/${entityId}/suppliers`, {
      method: 'POST', body: JSON.stringify(supplierForm),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
    suppliers.value.push(supplier)
    suppliers.value.sort((a, b) => a.razon_social.localeCompare(b.razon_social, 'es'))
    documentForm.supplier_id = supplier.id
    documentForm.issuer_cuit = supplier.cuit
    supplierForm.razon_social = ''
    supplierForm.cuit = ''
    showSupplier.value = false
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  } finally {
    finishBusy(busyToken)
  }
}

function selectSupplier() {
  const supplier = suppliers.value.find((item) => item.id === documentForm.supplier_id)
  if (supplier) documentForm.issuer_cuit = supplier.cuit
}

async function uploadForOcr() {
  if (!selectedEntityId.value || !selectedOutletId.value || !intakeFile.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const outletId = selectedOutletId.value
  const busyToken = startBusy()
  error.value = ''
  try {
    const data = new FormData()
    data.append('archivo', intakeFile.value)
    const operation = await request<IntakeOperation>(`/entities/${entityId}/outlets/${outletId}/intake`, {
      method: 'POST', body: data,
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId) || outletId !== selectedOutletId.value) return
    intakeOperation.value = operation
    ocrDraft.value = null
    ocrDisclosureAcceptedVersion.value = null
    createSupplierOnConfirm.value = false
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  } finally {
    finishBusy(busyToken)
  }
}

async function runOcr() {
  if (
    !selectedEntityId.value
    || !intakeOperation.value
    || !ocrConfig.value.version_aviso
    || ocrDisclosureAcceptedVersion.value !== ocrConfig.value.version_aviso
  ) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const operationId = intakeOperation.value.id
  const disclosureVersion = ocrDisclosureAcceptedVersion.value
  const busyToken = startBusy()
  error.value = ''
  ocrDisclosureAcceptedVersion.value = null
  try {
    const result = await request<{ estado: string; borrador: OcrDraft }>(`/entities/${entityId}/intake/${operationId}/ocr`, {
      method: 'POST',
      body: JSON.stringify({ acepta_envio_google: true, version_aviso: disclosureVersion }),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId) || intakeOperation.value?.id !== operationId) return
    ocrDraft.value = result.borrador
    documentForm.supplier_id = 0
    documentForm.issuer_cuit = result.borrador.cuit_emisor ?? ''
    documentForm.tipo = result.borrador.tipo ?? 'invoice'
    documentForm.letra = result.borrador.letra
    documentForm.punto_venta = result.borrador.punto_venta ?? 1
    documentForm.numero = result.borrador.numero ?? 1
    documentForm.fecha_emision = result.borrador.fecha_emision ?? todayInBuenosAires()
    documentForm.fecha_vencimiento = result.borrador.fecha_vencimiento ?? ''
    documentForm.moneda = result.borrador.moneda ?? 'ARS'
    documentForm.neto = result.borrador.neto ?? ''
    documentForm.iva = result.borrador.iva ?? ''
    documentForm.total = result.borrador.total ?? ''
    Object.assign(supplierForm, {
      razon_social: result.borrador.razon_social_proveedor ?? '',
      cuit: result.borrador.cuit_emisor ?? '',
    })
    showCreate.value = true
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  } finally {
    finishBusy(busyToken)
  }
}

function useSuggestedSupplier() {
  const match = ocrDraft.value?.proveedor.coincidencia
  if (!match) return
  documentForm.supplier_id = match.id
  documentForm.issuer_cuit = match.cuit
  createSupplierOnConfirm.value = false
}

async function discardIntake() {
  if (!selectedEntityId.value || !intakeOperation.value) return
  const entityId = selectedEntityId.value
  const operationId = intakeOperation.value.id
  const contextToken = contextGeneration
  try {
    await request(`/entities/${entityId}/intake/${operationId}`, { method: 'DELETE' })
    if (contextToken !== contextGeneration || entityId !== selectedEntityId.value) return
    intakeOperation.value = null
    intakeFile.value = null
    ocrDraft.value = null
    ocrDisclosureAcceptedVersion.value = null
    createSupplierOnConfirm.value = false
    resetForms()
    documentForm.supplier_id = suppliers.value[0]?.id ?? 0
    documentForm.issuer_cuit = suppliers.value[0]?.cuit ?? ''
  } catch (value) {
    if (contextToken === contextGeneration && entityId === selectedEntityId.value) showError(value)
  }
}

async function toggleCreate() {
  if (showCreate.value && intakeOperation.value) {
    await discardIntake()
    if (intakeOperation.value) return
  }
  showCreate.value = !showCreate.value
}

async function confirmOcrDraft() {
  if (!selectedEntityId.value || !selectedOutletId.value || !intakeOperation.value || !ocrDraft.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const outletId = selectedOutletId.value
  const operationId = intakeOperation.value.id
  const busyToken = startBusy()
  error.value = ''
  try {
    const created = await request<Document>(`/entities/${entityId}/intake/${operationId}/confirm`, {
      method: 'POST',
      body: JSON.stringify({
        ...documentForm,
        outlet_id: outletId,
        supplier_id: createSupplierOnConfirm.value ? null : documentForm.supplier_id,
        crear_proveedor: createSupplierOnConfirm.value,
        razon_social_proveedor: supplierForm.razon_social,
        fecha_vencimiento: documentForm.fecha_vencimiento || null,
        neto: documentForm.neto || null,
        iva: documentForm.iva || null,
      }),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId) || outletId !== selectedOutletId.value) return
    intakeOperation.value = null
    intakeFile.value = null
    ocrDraft.value = null
    ocrDisclosureAcceptedVersion.value = null
    createSupplierOnConfirm.value = false
    showCreate.value = false
    await loadContext(sessionToken)
    if (contextIsCurrent(sessionToken, contextGeneration, entityId)) await openDocument(created.id)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  } finally {
    finishBusy(busyToken)
  }
}

async function createDocument() {
  if (!selectedEntityId.value || !selectedOutletId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const outletId = selectedOutletId.value
  const busyToken = startBusy()
  error.value = ''
  try {
    const created = await request<Document>(`/entities/${entityId}/documents`, {
      method: 'POST',
      body: JSON.stringify({
        ...documentForm,
        outlet_id: outletId,
        fecha_vencimiento: documentForm.fecha_vencimiento || null,
      }),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId) || outletId !== selectedOutletId.value) return
    showCreate.value = false
    documentForm.numero += 1
    documentForm.neto = ''
    documentForm.iva = ''
    documentForm.total = ''
    documentForm.descripcion = ''
    await loadDocuments(sessionToken, contextToken)
    if (contextIsCurrent(sessionToken, contextToken, entityId)) await openDocument(created.id)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  } finally {
    finishBusy(busyToken)
  }
}

async function uploadAttachment() {
  if (!selectedEntityId.value || !selectedDocument.value || !uploadFile.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const documentId = selectedDocument.value.id
  const busyToken = startBusy()
  error.value = ''
  try {
    const data = new FormData()
    data.append('archivo', uploadFile.value)
    await request(`/entities/${entityId}/documents/${documentId}/attachments`, {
      method: 'POST', body: data,
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId) || selectedDocument.value?.id !== documentId) return
    uploadFile.value = null
    await openDocument(documentId)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  } finally {
    finishBusy(busyToken)
  }
}

async function reviewDocument() {
  if (!selectedEntityId.value || !selectedDocument.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const documentId = selectedDocument.value.id
  const expectedRevision = selectedDocument.value.revision
  const motivo = window.prompt('Motivo de la revisión:')?.trim()
  if (!motivo) return
  try {
    const reviewed = await request<Document>(
      `/entities/${entityId}/documents/${documentId}/review`,
      { method: 'POST', body: JSON.stringify({ motivo, expected_revision: expectedRevision }) },
    )
    if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
    selectedDocument.value = reviewed
    await loadDocuments(sessionToken, contextToken)
    if (contextIsCurrent(sessionToken, contextToken, entityId)) await openDocument(documentId)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function deleteDocument() {
  if (!selectedEntityId.value || !selectedDocument.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const documentId = selectedDocument.value.id
  const expectedRevision = selectedDocument.value.revision
  const motivo = window.prompt('Motivo de la baja:')?.trim()
  if (!motivo) return
  if (!window.confirm('¿Querés dar de baja este comprobante?')) return
  try {
    await request(`/entities/${entityId}/documents/${documentId}`, {
      method: 'DELETE', body: JSON.stringify({ motivo, expected_revision: expectedRevision }),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
    selectedDocument.value = null
    await loadDocuments(sessionToken, contextToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function downloadAttachment(id: number, name: string) {
  if (!selectedEntityId.value || !selectedDocument.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const documentId = selectedDocument.value.id
  const isCurrent = () => contextIsCurrent(sessionToken, contextToken, entityId) && selectedDocument.value?.id === documentId
  try {
    await download(`/entities/${entityId}/documents/${documentId}/attachments/${id}`, name, isCurrent)
  } catch (value) {
    if (isCurrent()) showError(value)
  }
}

async function loadAccountsPayable(sessionToken = sessionGeneration, contextToken = contextGeneration) {
  if (!selectedEntityId.value || !canRecordPayments.value) return
  const entityId = selectedEntityId.value
  const [nextPayables, nextPayments] = await Promise.all([
    request<Payable[]>(`/entities/${entityId}/payables`),
    request<Payment[]>(`/entities/${entityId}/payments`),
  ])
  if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
  payables.value = nextPayables
  payments.value = nextPayments
  if (!paymentForm.supplier_id) paymentForm.supplier_id = suppliers.value[0]?.id ?? 0
}

async function toggleAccountsPayable() {
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  showAccountsPayable.value = !showAccountsPayable.value
  if (!showAccountsPayable.value || !entityId) return
  try {
    await loadAccountsPayable(sessionToken, contextToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function postSelectedDocumentToPayable() {
  if (!selectedEntityId.value || !selectedDocument.value) return
  const motivo = window.prompt('Motivo del alta en cuentas a pagar:')?.trim()
  if (!motivo) return
  const entityId = selectedEntityId.value
  const documentId = selectedDocument.value.id
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  try {
    await request(`/entities/${entityId}/documents/${documentId}/payable`, {
      method: 'POST', body: JSON.stringify({ motivo }),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId) || selectedDocument.value?.id !== documentId) return
    await loadAccountsPayable(sessionToken, contextToken)
    if (contextIsCurrent(sessionToken, contextToken, entityId)) await openDocument(documentId)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function createPaymentDraft() {
  if (!selectedEntityId.value || !selectedOutletId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const outletId = selectedOutletId.value
  const paymentDetailToken = ++paymentDetailGeneration
  paymentReceipt.value = null
  try {
    const created = await request<Payment>(`/entities/${entityId}/payments`, {
      method: 'POST',
      body: JSON.stringify({ ...paymentForm, outlet_id: outletId }),
    })
    if (
      !contextIsCurrent(sessionToken, contextToken, entityId)
      || selectedOutletId.value !== outletId
      || paymentDetailToken !== paymentDetailGeneration
    ) return
    paymentDraft.value = created
    await loadAccountsPayable(sessionToken, contextToken)
  } catch (value) {
    if (
      contextIsCurrent(sessionToken, contextToken, entityId)
      && paymentDetailToken === paymentDetailGeneration
    ) showError(value)
  }
}

async function openPayment(paymentId: number) {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const paymentDetailToken = ++paymentDetailGeneration
  paymentReceipt.value = null
  try {
    const detail = await request<Payment>(`/entities/${entityId}/payments/${paymentId}`)
    if (
      !contextIsCurrent(sessionToken, contextToken, entityId)
      || paymentDetailToken !== paymentDetailGeneration
    ) return
    paymentDraft.value = detail
    Object.assign(paymentForm, {
      supplier_id: detail.supplier.id,
      moneda: detail.moneda,
      total: detail.total,
      metodo: detail.metodo,
      referencia: detail.referencia,
    })
    Object.keys(allocationAmounts).forEach((key) => delete allocationAmounts[Number(key)])
    detail.aplicaciones?.forEach((item) => { allocationAmounts[item.payable_id] = item.importe })
  } catch (value) {
    if (
      contextIsCurrent(sessionToken, contextToken, entityId)
      && paymentDetailToken === paymentDetailGeneration
    ) showError(value)
  }
}

function clearPaymentDraft() {
  paymentDetailGeneration += 1
  paymentDraft.value = null
  paymentReceipt.value = null
  Object.keys(allocationAmounts).forEach((key) => delete allocationAmounts[Number(key)])
}

async function postCurrentPayment() {
  if (!selectedEntityId.value || !paymentDraft.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const paymentId = paymentDraft.value.id
  const idempotencyKey = paymentDraft.value.idempotency_key
  const aplicaciones = eligiblePayables.value
    .filter((item) => allocationAmounts[item.id]?.trim())
    .map((item) => ({ payable_id: item.id, importe: allocationAmounts[item.id] }))
  if (!window.confirm('¿Querés registrar este pago? Una vez registrado solo se corrige con una reversión.')) return
  try {
    const posted = await request<Payment>(
      `/entities/${entityId}/payments/${paymentId}/post`,
      { method: 'POST', body: JSON.stringify({ idempotency_key: idempotencyKey, aplicaciones }) },
    )
    if (!contextIsCurrent(sessionToken, contextToken, entityId) || paymentDraft.value?.id !== paymentId) return
    paymentDraft.value = posted
    await loadAccountsPayable(sessionToken, contextToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId) && paymentDraft.value?.id === paymentId) showError(value)
  }
}

async function reverseSupplierPayment(payment: Payment) {
  if (!selectedEntityId.value) return
  const motivo = window.prompt('Motivo de la reversión:')?.trim()
  if (!motivo) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const paymentId = payment.id
  try {
    await request(`/entities/${entityId}/payments/${paymentId}/reverse`, {
      method: 'POST', body: JSON.stringify({ motivo }),
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId)) return
    await loadAccountsPayable(sessionToken, contextToken)
    if (paymentDraft.value?.id === paymentId) {
      paymentDraft.value = payments.value.find((item) => item.id === paymentId) ?? paymentDraft.value
    }
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId)) showError(value)
  }
}

async function uploadPaymentReceipt() {
  if (!selectedEntityId.value || !paymentDraft.value || !paymentReceipt.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const paymentId = paymentDraft.value.id
  const receiptFile = paymentReceipt.value
  const form = new FormData()
  form.append('archivo', receiptFile)
  try {
    await request(`/entities/${entityId}/payments/${paymentId}/receipt`, {
      method: 'POST', body: form,
    })
    if (!contextIsCurrent(sessionToken, contextToken, entityId) || paymentDraft.value?.id !== paymentId) return
    paymentDraft.value.tiene_comprobante = true
    paymentReceipt.value = null
    await loadAccountsPayable(sessionToken, contextToken)
  } catch (value) {
    if (contextIsCurrent(sessionToken, contextToken, entityId) && paymentDraft.value?.id === paymentId) showError(value)
  }
}

async function downloadPaymentReceipt(payment: Payment) {
  if (!selectedEntityId.value) return
  const sessionToken = sessionGeneration
  const contextToken = contextGeneration
  const entityId = selectedEntityId.value
  const paymentId = payment.id
  const isCurrent = () => contextIsCurrent(sessionToken, contextToken, entityId)
  try {
    await download(
      `/entities/${entityId}/payments/${paymentId}/receipt`,
      `comprobante-pago-${paymentId}`,
      isCurrent,
    )
  } catch (value) {
    if (isCurrent()) showError(value)
  }
}

onMounted(() => { if (!publicVenue) return initialize() })
</script>

<template>
  <PublicReservations v-if="publicVenue" :venue="publicVenue" />

  <div v-else-if="loading" class="center-page" aria-live="polite">Cargando…</div>

  <main v-else-if="!authenticated" class="login-page">
    <section class="login-card">
      <div class="appearance-toolbar"><ThemeSelect /><InstallApp /></div>
      <div class="brand-mark">G</div>
      <p class="eyebrow">GOMAR</p>
      <h1>Registro de compras</h1>
      <p class="muted">Ingresá con tu cuenta asignada. No hay registro público.</p>
      <form class="stack" @submit.prevent="signIn">
        <label>Usuario<input v-model="loginForm.usuario" autocomplete="username" required /></label>
        <label>Contraseña<input v-model="loginForm.clave" type="password" autocomplete="current-password" required /></label>
        <p v-if="error" class="error" role="alert">{{ error }}</p>
        <button class="primary" :disabled="busy">{{ busy ? 'Ingresando…' : 'Ingresar' }}</button>
      </form>
    </section>
  </main>

  <div v-else class="app-shell">
    <header class="topbar">
      <div><span class="brand-mark small">G</span><strong>Gomar · Compras</strong></div>
      <div class="session"><span>{{ username }}</span><ThemeSelect /><InstallApp /><button class="text-button" @click="signOut">Cerrar sesión</button></div>
    </header>

    <main class="workspace">
      <section v-if="entities.length" class="context card staff-context" aria-label="Lugar de trabajo">
        <label>Entidad
          <select v-model="selectedEntityId" aria-label="Entidad" @change="changeEntity">
            <option v-for="entity in entities" :key="entity.id" :value="entity.id">{{ entity.nombre }}</option>
          </select>
        </label>
        <label>Local
          <select v-model="selectedOutletId" aria-label="Local" :disabled="!outlets.length" @change="changeOutlet">
            <option v-if="!outlets.length" :value="null">Sin locales asignados</option>
            <option v-for="outlet in outlets" :key="outlet.id" :value="outlet.id">{{ outlet.nombre }}</option>
          </select>
        </label>
        <span v-if="currentEntity" class="staff-role">{{ currentEntity.rol_nombre }} · CUIT {{ currentEntity.cuit }}</span>
      </section>

      <p v-if="error" class="error banner" role="alert">{{ error }} <button @click="error = ''">Cerrar</button></p>
      <p v-if="entities.length === 0" class="empty card">No tenés accesos a entidades asignados.</p>

      <template v-else>
        <nav class="staff-nav" aria-label="Tareas del local">
          <button type="button" :aria-current="staffView === 'documents' ? 'page' : undefined" @click="selectStaffView('documents')">Comprobantes</button>
          <button v-if="canRecordPayments" type="button" :aria-current="staffView === 'payables' ? 'page' : undefined" @click="selectStaffView('payables')">Cuentas a pagar</button>
          <button v-if="canReconcile" type="button" :aria-current="staffView === 'reconciliation' ? 'page' : undefined" @click="selectStaffView('reconciliation')">Conciliación financiera</button>
          <button v-if="hasOutlet && canReadStock" type="button" :aria-current="staffView === 'stock' ? 'page' : undefined" @click="selectStaffView(staffView === 'stock' ? 'documents' : 'stock')">{{ staffView === 'stock' ? 'Cerrar stock del local' : 'Ver stock del local' }}</button>
          <button v-if="hasOutlet && canReadDelivery" type="button" :aria-current="staffView === 'delivery' ? 'page' : undefined" @click="selectStaffView(staffView === 'delivery' ? 'documents' : 'delivery')">{{ staffView === 'delivery' ? 'Cerrar bandeja de delivery' : 'Abrir bandeja de delivery' }}</button>
          <button v-if="hasOutlet && canManageAccess" type="button" :aria-current="staffView === 'reservations' ? 'page' : undefined" @click="selectStaffView(staffView === 'reservations' ? 'documents' : 'reservations')">{{ staffView === 'reservations' ? 'Cerrar reservas' : 'Ver reservas del local' }}</button>
          <button v-if="hasOutlet && canReadBusiness" type="button" :aria-current="staffView === 'knowledge' ? 'page' : undefined" @click="selectStaffView(staffView === 'knowledge' ? 'documents' : 'knowledge')">{{ staffView === 'knowledge' ? 'Cerrar manual del local' : 'Abrir manual del local' }}</button>
          <button v-if="canUseChat" type="button" :aria-current="staffView === 'chat' ? 'page' : undefined" @click="selectStaffView('chat')">Chat y propuestas</button>
          <details v-if="canManageAccess || canManageSuppliers" class="staff-admin">
            <summary>Administración</summary>
            <div class="staff-admin-items">
              <button v-if="canManageAccess" type="button" :aria-current="staffView === 'access' ? 'page' : undefined" @click="staffView === 'access' ? selectStaffView('documents') : selectStaffView('access')">{{ staffView === 'access' ? 'Cerrar accesos' : 'Gestionar accesos' }}</button>
              <button v-if="canManageSuppliers" type="button" :aria-current="staffView === 'suppliers' ? 'page' : undefined" @click="staffView === 'suppliers' ? selectStaffView('documents') : selectStaffView('suppliers')">{{ staffView === 'suppliers' ? 'Cerrar proveedores' : 'Gestionar proveedores' }}</button>
            </div>
          </details>
        </nav>
        <header class="staff-view-heading" :class="{ 'staff-mobile-detail': showDocumentDetail }">
          <h1 ref="staffHeading" tabindex="-1">{{ viewNames[staffView] }}</h1>
          <button v-if="staffView === 'documents' && canManageDrafts" class="new-button" :class="showCreate ? 'secondary' : 'primary'" :disabled="busy" @click="toggleCreate">
            {{ showCreate ? 'Cancelar alta' : 'Nuevo comprobante' }}
          </button>
        </header>

        <section v-if="staffView === 'chat' && canUseChat" class="card staff-chat" aria-label="Accesos y propuestas de chat">
          <McpConnect
            v-if="canConnectMCP && selectedEntityId && selectedOutletId && outlets.some((item) => item.id === selectedOutletId)"
            :key="`mcp-${selectedEntityId}-${selectedOutletId}`"
            :entity="selectedEntityId"
            :outlet="selectedOutletId"
            :outlet-name="outlets.find((item) => item.id === selectedOutletId)?.nombre ?? 'este local'"
          />
          <McpWikiProposals
            v-if="canReviewWikiProposals && selectedEntityId && selectedOutletId && outlets.some((item) => item.id === selectedOutletId)"
            :key="`wiki-proposals-${selectedEntityId}-${selectedOutletId}`"
            :entity="selectedEntityId"
            :outlet="selectedOutletId"
            :outlet-name="outlets.find((item) => item.id === selectedOutletId)?.nombre ?? 'este local'"
          />
          <McpWikiEditProposals
            v-if="canReviewWikiEditProposals && selectedEntityId && selectedOutletId && outlets.some((item) => item.id === selectedOutletId)"
            :key="`wiki-edit-proposals-${selectedEntityId}-${selectedOutletId}`"
            :entity="selectedEntityId"
            :outlet="selectedOutletId"
            :outlet-name="outlets.find((item) => item.id === selectedOutletId)?.nombre ?? 'este local'"
          />
          <McpDocumentProposals
            v-if="canReviewDocumentProposals && selectedEntityId && selectedOutletId && outlets.some((item) => item.id === selectedOutletId)"
            :key="`document-proposals-${selectedEntityId}-${selectedOutletId}`"
            :entity="selectedEntityId"
            :outlet="selectedOutletId"
            :outlet-name="outlets.find((item) => item.id === selectedOutletId)?.nombre ?? 'este local'"
          />
          <McpStockReceiptProposals
            v-if="canReviewStockReceiptProposals && selectedEntityId && selectedOutletId && outlets.some((item) => item.id === selectedOutletId)"
            :key="`stock-receipt-proposals-${selectedEntityId}-${selectedOutletId}`"
            :entity="selectedEntityId"
            :outlet="selectedOutletId"
            :outlet-name="outlets.find((item) => item.id === selectedOutletId)?.nombre ?? 'este local'"
          />
          <McpProposalSessions
            v-if="canManageMcpProposalSessions && selectedEntityId && selectedOutletId && outlets.some((item) => item.id === selectedOutletId)"
            :key="`proposal-sessions-${selectedEntityId}-${selectedOutletId}`"
            :can-rename="canRenameViaMcp"
            :can-review-documents="canReviewDocViaMcp"
            :can-create-reconciliation-draft="canCreateReconciliationViaMcp"
            :can-edit-documents="canEditDocViaMcp"
            :can-adjust-stock="canAdjustStockViaMcp"
            :can-change-catalog="canChangeCatalogViaMcp"
            :can-confirm-reservations="canConfirmReservationsViaMcp"
            :entity="selectedEntityId"
            :outlet="selectedOutletId"
            :outlet-name="outlets.find((item) => item.id === selectedOutletId)?.nombre ?? 'este local'"
          />
        </section>
        <StockPanel
          v-if="showStock && selectedEntityId && selectedOutletId && canReadStock"
          :key="`stock-${selectedEntityId}-${selectedOutletId}`"
          :entity="selectedEntityId"
          :outlet="selectedOutletId"
        />
        <DeliveryInbox
          v-if="showDelivery && selectedEntityId && selectedOutletId && canReadDelivery"
          :key="`delivery-${selectedEntityId}-${selectedOutletId}`"
          :entity="selectedEntityId"
          :outlet="selectedOutletId"
        />
        <StaffReservations
          v-if="showReservations && selectedEntityId && selectedOutletId && canManageAccess"
          :key="`reservations-${selectedEntityId}-${selectedOutletId}`"
          :entity="selectedEntityId"
          :outlet="selectedOutletId"
          :can-edit="canManageAccess"
        />
        <KnowledgePanel
          v-if="showKnowledge && selectedEntityId && selectedOutletId && canReadBusiness"
          :key="`knowledge-${selectedEntityId}-${selectedOutletId}`"
          :entity="selectedEntityId"
          :outlet="selectedOutletId"
          :can-edit="canManageAccess"
        />
        <section v-if="showManagement && management" class="card management-card">
          <div class="section-heading"><div><p class="eyebrow">ENTIDAD</p><h2>Accesos y locales</h2></div></div>
          <form class="management-row" @submit.prevent="saveEntity">
            <label>Razón social<input v-model="management.entidad.nombre" required /></label>
            <label>CUIT<input v-model="management.entidad.cuit" maxlength="14" required /></label>
            <button class="secondary">Guardar entidad</button>
          </form>
          <div class="management-section">
            <h3>Locales</h3>
            <form class="management-row" @submit.prevent="createOutlet">
              <label>Nuevo local<input v-model="newOutletForm.nombre" required /></label>
              <label class="check"><input v-model="newOutletForm.activa" type="checkbox" /> Activo</label>
              <button class="secondary">Crear local</button>
            </form>
            <div v-for="outlet in management.locales" :key="outlet.id" class="management-row compact">
              <label>Nombre<input v-model="outlet.nombre" :disabled="!outlet.autorizado" /></label>
              <label class="check"><input v-model="outlet.activa" type="checkbox" :disabled="!outlet.autorizado" /> Activo</label>
              <button class="secondary" :disabled="!outlet.autorizado" @click="saveOutlet(outlet)">Guardar</button>
              <small v-if="!outlet.autorizado" class="muted">Sin autorización para este local</small>
            </div>
          </div>
          <div class="management-section">
            <h3>Membresías</h3>
            <p class="muted">Ingresá el nombre de usuario exacto. Las cuentas se crean desde la administración de plataforma.</p>
            <form class="membership-card" @submit.prevent="createMembership">
              <label>Usuario<input v-model="newMembershipForm.usuario" autocomplete="off" required /></label>
              <label>Rol<select v-model="newMembershipForm.rol"><option v-for="role in management.roles" :key="role.valor" :value="role.valor">{{ role.nombre }}</option></select></label>
              <label class="check"><input v-model="newMembershipForm.activa" type="checkbox" /> Activa</label>
              <fieldset><legend>Locales autorizados</legend><label v-for="outlet in management.locales.filter((item) => item.autorizado)" :key="outlet.id" class="check"><input v-model="newMembershipForm.outlet_ids" type="checkbox" :value="outlet.id" /> {{ outlet.nombre }}</label></fieldset>
              <button class="secondary">Agregar membresía</button>
            </form>
            <div v-for="item in management.membresias" :key="item.id" class="membership-card">
              <strong>{{ item.usuario }}</strong>
              <label>Rol<select v-model="item.rol" :disabled="!item.editable"><option v-for="role in management.roles" :key="role.valor" :value="role.valor">{{ role.nombre }}</option></select></label>
              <label class="check"><input v-model="item.activa" type="checkbox" :disabled="!item.editable" /> Activa</label>
              <fieldset><legend>Locales autorizados</legend><label v-for="outlet in management.locales.filter((value) => value.autorizado)" :key="outlet.id" class="check"><input v-model="item.outlet_ids" type="checkbox" :value="outlet.id" :disabled="!item.editable" /> {{ outlet.nombre }}</label></fieldset>
              <button class="secondary" :disabled="!item.editable" @click="saveMembership(item)">Guardar acceso</button>
              <small v-if="item.usuario === username" class="muted">Tu propia membresía no se puede cambiar desde acá.</small>
              <small v-else-if="!item.editable" class="muted">Esta membresía incluye una cuenta de plataforma o locales fuera de tu alcance.</small>
            </div>
          </div>
        </section>

        <section v-show="staffView === 'documents'" class="summary-grid staff-totals" :class="{ 'staff-mobile-detail': showDocumentDetail || showCreate }">
          <article class="metric card"><span>Comprobantes</span><strong>{{ total.cantidad }}</strong></article>
          <article v-for="row in total.totales" :key="row.moneda" class="metric card"><span>Total {{ row.moneda }}</span><strong>{{ money(row.total, row.moneda) }}</strong></article>
        </section>

        <ReconciliationPanel
          v-if="showReconciliation && canReconcile && selectedEntityId"
          :entity-id="selectedEntityId"
          :outlets="outlets"
          :selected-outlet-id="selectedOutletId"
          @error="showError"
        />

        <section v-if="showAccountsPayable && canRecordPayments" class="card ap-panel">
          <div class="section-heading"><div><p class="eyebrow">TESORERÍA</p><h2>Cuentas a pagar</h2></div><span>Solo facturas registradas de forma explícita</span></div>
          <div class="ap-grid">
            <div class="ap-column">
              <h3>Facturas registradas</h3>
              <div v-for="item in payables" :key="item.id" class="ap-row">
                <span><strong>{{ item.supplier.razon_social }}</strong><small>{{ item.referencia }} · {{ item.outlet.nombre }}</small></span>
                <span><strong>{{ money(item.saldo, item.moneda) }}</strong><small v-if="item.fecha_vencimiento">Vence {{ localDate(item.fecha_vencimiento) }}</small><small v-else>Sin vencimiento informado</small></span>
                <span class="status" :class="{ overdue: item.vencida, reviewed: item.estado === 'settled' }">{{ item.vencida ? 'Vencida' : item.estado_nombre }}</span>
              </div>
              <p v-if="!payables.length" class="empty">No hay facturas registradas en cuentas a pagar.</p>
            </div>
            <div class="ap-column payment-box">
              <h3>Nuevo pago manual</h3>
              <p class="muted">Efectivo y transferencia son solo etiquetas. Gomar no ejecuta movimientos bancarios.</p>
              <form v-if="!paymentDraft" class="stack" @submit.prevent="createPaymentDraft">
                <label>Proveedor<select v-model="paymentForm.supplier_id" required><option v-for="supplier in suppliers" :key="supplier.id" :value="supplier.id">{{ supplier.razon_social }}</option></select></label>
                <label>Moneda<select v-model="paymentForm.moneda"><option value="ARS">ARS</option><option value="USD">USD</option></select></label>
                <label>Total<input v-model="paymentForm.total" inputmode="decimal" placeholder="0,00" required /></label>
                <label>Medio<select v-model="paymentForm.metodo"><option value="cash">Efectivo</option><option value="bank_transfer">Transferencia bancaria</option></select></label>
                <label>Referencia manual<input v-model="paymentForm.referencia" maxlength="200" /></label>
                <button class="primary">Crear borrador</button>
              </form>
              <div v-else class="stack">
                <p><strong>Borrador #{{ paymentDraft.id }}</strong> · {{ money(paymentDraft.total, paymentDraft.moneda) }} · {{ paymentDraft.metodo_nombre }}</p>
                <template v-if="paymentDraft.estado === 'draft'">
                  <label v-for="item in eligiblePayables" :key="item.id">{{ item.referencia }} · saldo {{ money(item.saldo, item.moneda) }}
                    <input v-model="allocationAmounts[item.id]" inputmode="decimal" placeholder="Importe a aplicar" />
                  </label>
                  <p><strong>Aplicado:</strong> {{ money(allocatedTotal.toFixed(2), paymentDraft.moneda) }} / {{ money(paymentDraft.total, paymentDraft.moneda) }}</p>
                  <button class="primary" @click="postCurrentPayment">Registrar pago</button>
                </template>
                <p v-else-if="paymentDraft.estado === 'posted'" class="ocr-status">Pago registrado. Para corregirlo, registrá una reversión.</p>
                <p v-else class="ocr-status">Pago revertido.</p>
                <form v-if="!paymentDraft.tiene_comprobante" :key="paymentDraft.id" class="upload" @submit.prevent="uploadPaymentReceipt">
                  <label class="file-picker">{{ paymentReceipt?.name ?? 'Adjuntar comprobante privado' }}<input type="file" accept=".pdf,.png,.jpg,.jpeg" required @change="paymentReceipt = ($event.target as HTMLInputElement).files?.[0] ?? null" /></label>
                  <button class="secondary" :disabled="!paymentReceipt">Adjuntar</button>
                </form>
                <button v-else class="secondary" @click="downloadPaymentReceipt(paymentDraft)">Descargar comprobante</button>
                <button class="text-button" @click="clearPaymentDraft">Preparar otro pago</button>
              </div>
            </div>
          </div>
          <div class="subsection"><h3>Historial de pagos</h3>
            <div v-for="payment in payments" :key="payment.id" class="ap-row">
              <span><strong>#{{ payment.id }} · {{ payment.supplier.razon_social }}</strong><small>{{ payment.metodo_nombre }} · {{ payment.referencia || 'Sin referencia' }}</small></span>
              <span><strong>{{ money(payment.total, payment.moneda) }}</strong><small>{{ payment.tipo === 'reversal' ? `Reversión de #${payment.reversion_de}` : payment.estado_nombre }}</small></span>
              <span class="row-actions"><button class="text-button" @click="openPayment(payment.id)">Abrir</button><button v-if="payment.tiene_comprobante" class="text-button" @click="downloadPaymentReceipt(payment)">Comprobante</button><button v-if="canReversePayments && payment.estado === 'posted' && payment.tipo === 'payment'" class="danger" @click="reverseSupplierPayment(payment)">Revertir</button></span>
            </div>
            <p v-if="!payments.length" class="empty">Todavía no hay pagos.</p>
          </div>
        </section>

        <section v-if="showSupplierManagement" class="card supplier-management">
          <div class="section-heading"><div><p class="eyebrow">PROVEEDORES</p><h2>Datos de proveedores</h2></div></div>
          <div v-for="supplier in suppliers" :key="supplier.id" class="management-row compact">
            <label>Razón social<input v-model="supplier.razon_social" /></label>
            <label>CUIT<input v-model="supplier.cuit" maxlength="14" /></label>
            <button class="secondary" @click="saveSupplier(supplier)">Guardar</button>
          </div>
          <p v-if="!suppliers.length" class="empty">No hay proveedores para administrar.</p>
        </section>

        <section v-if="showCreate" v-show="staffView === 'documents'" class="card form-card">
          <div class="section-heading"><div><p class="eyebrow">ALTA</p><h2>Nuevo comprobante</h2></div></div>
          <section class="ocr-intake" aria-label="Carga asistida por OCR">
            <div><h3>Cargar PDF o foto</h3><p class="muted">PDF, PNG o JPEG. Máximo 10 MB y 8 páginas. La carga queda privada y no inicia OCR.</p></div>
            <p class="ocr-status">{{ ocrConfig.mensaje }}</p>
            <form v-if="ocrConfig.habilitado && !intakeOperation" class="upload" @submit.prevent="uploadForOcr">
              <label class="file-picker">{{ intakeFile?.name ?? 'Elegir PDF o foto' }}
                <input aria-label="Archivo para OCR" type="file" accept=".pdf,.png,.jpg,.jpeg" required @change="intakeFile = ($event.target as HTMLInputElement).files?.[0] ?? null" />
              </label>
              <button class="secondary" :disabled="busy || !intakeFile">Preparar carga</button>
            </form>
            <div v-if="intakeOperation && !ocrDraft" class="ocr-consent">
              <p><strong>Antes de continuar:</strong> {{ ocrConfig.aviso }}</p>
              <label class="check"><input v-model="ocrDisclosureAcceptedVersion" type="checkbox" :true-value="ocrConfig.version_aviso" :false-value="null" /> Leí este aviso y acepto enviar este archivo a Google mediante Gemini API para obtener sugerencias editables.</label>
              <div class="form-actions"><button type="button" class="secondary" :disabled="busy" @click="discardIntake">Descartar archivo</button><button type="button" class="primary" :disabled="busy || !ocrConfig.version_aviso || ocrDisclosureAcceptedVersion !== ocrConfig.version_aviso" @click="runOcr">{{ busy ? 'Procesando…' : 'Analizar con Gemini API' }}</button></div>
            </div>
            <div v-if="ocrDraft" class="ocr-draft" aria-live="polite">
              <p><strong>Borrador sin guardar.</strong> Revisá y corregí todos los datos antes de confirmar.</p>
              <ul><li v-for="warning in ocrDraft.advertencias" :key="warning">{{ warning }}</li></ul>
              <p v-if="ocrDraft.cuit_receptor">CUIT receptor sugerido: {{ ocrDraft.cuit_receptor }}</p>
              <div v-if="ocrDraft.proveedor.coincidencia" class="supplier-proposal">
                <span>Coincidencia exacta en esta entidad: {{ ocrDraft.proveedor.coincidencia.razon_social }} · {{ ocrDraft.proveedor.coincidencia.cuit }}</span>
                <button type="button" class="secondary" @click="useSuggestedSupplier">Confirmar proveedor sugerido</button>
              </div>
              <label class="check"><input v-model="createSupplierOnConfirm" type="checkbox" /> {{ ocrDraft.proveedor.coincidencia ? 'Crear otro proveedor con el CUIT emisor corregido al confirmar.' : 'Crear este proveedor al confirmar el comprobante.' }}</label>
            </div>
          </section>
          <form class="form-grid" @submit.prevent="ocrDraft ? confirmOcrDraft() : createDocument()">
            <label class="wide">Proveedor
              <select v-model="documentForm.supplier_id" :required="!createSupplierOnConfirm" @change="selectSupplier">
                <option :value="0" disabled>Seleccioná un proveedor</option>
                <option v-for="supplier in suppliers" :key="supplier.id" :value="supplier.id">{{ supplier.razon_social }} · {{ supplier.cuit }}</option>
              </select>
            </label>
            <button v-if="!ocrDraft" type="button" class="secondary align-end" @click="showSupplier = !showSupplier">Nuevo proveedor</button>
            <template v-if="showSupplier || (ocrDraft && createSupplierOnConfirm)">
              <label>Razón social<input v-model="supplierForm.razon_social" required /></label>
              <label v-if="!ocrDraft">CUIT del proveedor<input v-model="supplierForm.cuit" maxlength="14" required /></label>
              <button v-if="!ocrDraft" type="button" class="secondary align-end" :disabled="busy" @click="createSupplier">Guardar proveedor</button>
            </template>
            <label>CUIT emisor confirmado<input v-model="documentForm.issuer_cuit" maxlength="14" required /></label>
            <label>Tipo<select v-model="documentForm.tipo"><option value="invoice">Factura</option><option value="credit_note">Nota de crédito</option><option value="debit_note">Nota de débito</option></select></label>
            <label>Letra<input v-model="documentForm.letra" maxlength="1" pattern="[A-Za-z]?" /></label>
            <label>Punto de venta<input v-model.number="documentForm.punto_venta" type="number" min="1" required /></label>
            <label>Número<input v-model.number="documentForm.numero" type="number" min="1" required /></label>
            <label>Fecha de emisión<input v-model="documentForm.fecha_emision" type="date" required /></label>
            <label>Fecha de vencimiento<input v-model="documentForm.fecha_vencimiento" type="date" /></label>
            <label>Moneda<select v-model="documentForm.moneda"><option value="ARS">Peso argentino (ARS)</option><option value="USD">Dólar estadounidense (USD)</option></select></label>
            <label>Neto (opcional)<input v-model="documentForm.neto" inputmode="decimal" placeholder="Sin informar" /></label>
            <label>IVA (opcional)<input v-model="documentForm.iva" inputmode="decimal" placeholder="Sin informar" /></label>
            <label>Total<input v-model="documentForm.total" inputmode="decimal" placeholder="0,00" required /></label>
            <label class="full">Descripción<textarea v-model="documentForm.descripcion" rows="3" maxlength="2000" /></label>
            <div class="full form-actions"><button v-if="ocrDraft" type="button" class="secondary" :disabled="busy" @click="discardIntake">Rechazar borrador</button><button class="primary" :disabled="busy">{{ busy ? 'Guardando…' : ocrDraft ? 'Confirmar y guardar' : 'Guardar comprobante' }}</button></div>
          </form>
        </section>

        <div v-show="staffView === 'documents' && !showCreate" class="content-grid" :class="{ 'staff-has-document': showDocumentDetail }">
          <section class="card list-card">
            <div class="section-heading"><div><p class="eyebrow">REGISTRO</p><h2>Comprobantes</h2></div><span>{{ outlets.find((o) => o.id === selectedOutletId)?.nombre }}</span></div>
            <div v-if="documents.length" class="document-list">
              <button v-for="item in documents" :key="item.id" class="document-row" :class="{ active: selectedDocument?.id === item.id }" @click="openDocument(item.id)">
                <span><strong>{{ item.supplier.razon_social }}</strong><small>{{ item.referencia }}</small></span>
                <span class="row-end"><strong>{{ money(item.total, item.moneda) }}</strong><small>{{ localDate(item.fecha_emision) }}</small></span>
                <span class="status" :class="item.estado">{{ item.estado_nombre }}</span>
              </button>
            </div>
            <p v-else class="empty">Todavía no hay comprobantes en este local.</p>
          </section>

          <aside class="card detail-card">
            <template v-if="selectedDocument">
              <button type="button" class="text-button staff-back" @click="returnToDocuments">Volver a comprobantes</button>
              <div class="section-heading"><div><p class="eyebrow">DETALLE</p><h2 ref="documentHeading" tabindex="-1">{{ selectedDocument.tipo_nombre }}</h2></div><span class="status" :class="selectedDocument.estado">{{ selectedDocument.estado_nombre }}</span></div>
              <dl class="details">
                <div><dt>Proveedor</dt><dd>{{ selectedDocument.supplier.razon_social }}</dd></div>
                <div><dt>CUIT</dt><dd>{{ selectedDocument.supplier.cuit }}</dd></div>
                <div><dt>Referencia</dt><dd>{{ selectedDocument.referencia }}</dd></div>
                <div><dt>Emisión</dt><dd>{{ localDate(selectedDocument.fecha_emision) }}</dd></div>
                <div><dt>Neto</dt><dd>{{ money(selectedDocument.neto, selectedDocument.moneda) }}</dd></div>
                <div><dt>IVA</dt><dd>{{ money(selectedDocument.iva, selectedDocument.moneda) }}</dd></div>
                <div class="total"><dt>Total {{ selectedDocument.moneda }}</dt><dd>{{ money(selectedDocument.total, selectedDocument.moneda) }}</dd></div>
              </dl>
              <p v-if="selectedDocument.descripcion" class="description">{{ selectedDocument.descripcion }}</p>

              <div class="subsection"><h3>Adjuntos privados</h3>
                <ul v-if="selectedDocument.adjuntos?.length" class="attachment-list">
                  <li v-for="attachment in selectedDocument.adjuntos" :key="attachment.id">
                    <span>{{ attachment.nombre }}<small>{{ Math.ceil(attachment.tamano / 1024) }} KB</small></span>
                    <button v-if="canDownload" class="secondary" @click="downloadAttachment(attachment.id, attachment.nombre)">Descargar</button>
                  </li>
                </ul>
                <p v-else class="muted">Sin archivos adjuntos.</p>
                <form v-if="canManageSelectedDocument" class="upload" @submit.prevent="uploadAttachment">
                  <label class="file-picker">{{ uploadFile?.name ?? 'Elegir archivo' }}
                    <input aria-label="Archivo adjunto" type="file" accept=".pdf,.png,.jpg,.jpeg" required @change="uploadFile = ($event.target as HTMLInputElement).files?.[0] ?? null" />
                  </label>
                  <button class="secondary" :disabled="busy || !uploadFile">Subir archivo</button>
                  <small>PDF, PNG o JPEG. Máximo 10 MB y 8 páginas.</small>
                </form>
              </div>

              <div v-if="selectedDocument.auditoria?.length" class="subsection"><h3>Auditoría</h3>
                <ol class="audit-list"><li v-for="(event, index) in selectedDocument.auditoria" :key="index"><strong>{{ event.accion_nombre }}</strong> · {{ event.motivo }}<small>{{ event.actor }} · {{ localDateTime(event.fecha) }}</small></li></ol>
              </div>
              <div v-if="canReview || canManageSelectedDocument || canRecordPayments" class="detail-actions"><button v-if="canReview && selectedDocument.estado !== 'reviewed'" class="secondary" @click="reviewDocument">Marcar revisado</button><button v-if="canRecordPayments && selectedDocument.estado === 'reviewed' && !selectedDocument.payable_id && selectedDocument.tipo === 'invoice'" class="primary" @click="postSelectedDocumentToPayable">Registrar en cuentas a pagar</button><button v-if="canManageSelectedDocument && !selectedDocument.payable_id" class="danger" @click="deleteDocument">Dar de baja</button></div>
            </template>
            <div v-else class="detail-empty"><span>↗</span><p>Elegí un comprobante para ver el detalle y sus adjuntos.</p></div>
          </aside>
        </div>
      </template>
    </main>
  </div>
</template>
