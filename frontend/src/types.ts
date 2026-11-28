export interface Entity { id: number; nombre: string; cuit: string; rol: string; rol_nombre: string; capacidades: string[] }
export interface Outlet { id: number; nombre: string }
export interface Supplier { id: number; razon_social: string; cuit: string }
export interface ManagedOutlet extends Outlet { activa: boolean; autorizado: boolean }
export interface ManagedMembership {
  id: number
  usuario: string
  rol: string
  rol_nombre: string
  activa: boolean
  editable: boolean
  outlet_ids: number[]
}
export interface EntityManagement {
  entidad: { id: number; nombre: string; cuit: string }
  roles: { valor: string; nombre: string }[]
  locales: ManagedOutlet[]
  membresias: ManagedMembership[]
}
export interface Attachment { id: number; nombre: string; tipo: string; tamano: number; subido: string }
export interface Audit { accion: string; accion_nombre: string; actor: string; fecha: string; motivo: string }
export interface Document {
  id: number
  entity_id: number
  outlet: Outlet
  supplier: Supplier
  tipo: string
  tipo_nombre: string
  letra: string
  punto_venta: number
  numero: number
  referencia: string
  fecha_emision: string
  fecha_vencimiento: string | null
  moneda: string
  neto: string | null
  iva: string | null
  total: string
  descripcion: string
  estado: string
  estado_nombre: string
  revision: number
  creado: string
  actualizado: string
  payable_id: number | null
  adjuntos?: Attachment[]
  auditoria?: Audit[]
}

export interface Payable {
  id: number
  documento_id: number
  referencia: string
  outlet: Outlet
  supplier: Supplier
  fecha_emision: string
  fecha_vencimiento: string | null
  moneda: string
  importe_original: string
  saldo: string
  estado: string
  estado_nombre: string
  vencida: boolean
  registrada: string
  archivada?: boolean
  movimientos?: { tipo_nombre: string; variacion: string; saldo: string; fecha: string; actor: string; motivo: string }[]
}

export interface Payment {
  id: number
  outlet: Outlet
  supplier: Supplier
  moneda: string
  total: string
  metodo: string
  metodo_nombre: string
  referencia: string
  estado: string
  estado_nombre: string
  tipo: string
  reversion_de: number | null
  motivo_reversion: string
  creado: string
  registrado: string | null
  tiene_comprobante: boolean
  idempotency_key: string
  archivado?: boolean
  aplicaciones?: { payable_id: number; referencia: string; importe: string }[]
}

export interface OcrConfig {
  habilitado: boolean
  proveedor: string
  version_aviso: string
  aviso: string
  mensaje: string
}
export interface IntakeOperation { id: string; estado: string; tipo: string; tamano: number; paginas: number; vence: string }
export interface OcrDraft {
  razon_social_proveedor: string | null
  cuit_emisor: string | null
  cuit_receptor: string | null
  tipo: string | null
  letra: string
  punto_venta: number | null
  numero: number | null
  fecha_emision: string | null
  fecha_vencimiento: string | null
  moneda: string | null
  neto: string | null
  iva: string | null
  total: string | null
  desglose_impuestos: { label: string; amount: string }[] | null
  proveedor: { estado: string; coincidencia: Supplier | null }
  advertencias: string[]
}

export interface ReconciliationRun {
  id: number
  entity_id: number
  outlet_ids: number[]
  state: 'draft' | 'review' | 'closed'
  revision: number
  match_reference: boolean
  unresolved_count: number | null
  counts: { sources: number; rows: number; confirmed: number; rejected: number }
  sources?: ReconciliationSource[]
}
export interface ReconciliationSource {
  id: number
  side: 'payments' | 'cash_flow'
  label: string
  mapping: Record<string, number>
  row_count: number
}
export interface ReconciliationRow {
  id: number
  side: 'payments' | 'cash_flow'
  source: string
  record_number: number
  outlet_id: number
  date: string
  amount: string
  currency: string
  currency_source: 'imported' | 'supplied'
  category: string | null
  description: string | null
  payment_method: string | null
  reference: string | null
  vat_rate: string | null
  vat_amount: string | null
}
export interface ReconciliationCandidate {
  group_id: number
  kind: 'candidate' | 'ambiguous' | 'unmatched'
  payments: ReconciliationRow[]
  cash_flow: ReconciliationRow[]
  discrepancies?: string[]
  comparisons?: Record<string, 'equal' | 'different' | 'not_comparable'>
  payment_count: number
  cash_flow_count: number
  decisions?: { payment_row_id: number; cash_flow_row_id: number; decision: 'confirmed' | 'rejected' }[]
}
