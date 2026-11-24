import { capturedTillTime, type ReceiptClock } from './sale-time'

export interface Context {
  actor: number
  entity: number
  outlet: number
}
export interface Product {
  id: number
  nombre: string
  precio_centavos: number
  revision: number
  activo: boolean
  codigo_barras?: string // Absent in older cached catalogs and on uncoded products.
}
export interface Grant extends Context {
  lease: string
  expires: number
  label: string
  catalog: Product[]
  tableChecks?: boolean
  manualTender?: boolean
  tableFinalization?: boolean
  tillTimeV1?: boolean
}
export interface Line {
  producto_id: number
  revision: number
  cantidad: number
  nombre: string
  precio_centavos: number
}
export const paymentLabels = {
  debito: 'Tarjeta de débito',
  credito: 'Tarjeta de crédito',
  qr_modo: 'QR MODO',
  qr_mercado_pago: 'QR Mercado Pago',
  transferencia: 'Transferencia bancaria',
  efectivo: 'Efectivo',
} as const
export type PaymentMethod = keyof typeof paymentLabels

export function isPaymentMethod(value: unknown): value is PaymentMethod {
  return typeof value === 'string' && Object.hasOwn(paymentLabels, value)
}

export interface Sale extends Context {
  id: string
  key: string
  created: number
  tillReportedAt?: string // Captured at enqueue, never derived for an existing row.
  receiptClock?: ReceiptClock
  lines: Line[]
  total: number
  paymentMethod?: PaymentMethod // Absent in cash-only records from the earlier version.
  status: 'pending' | 'synced' | 'attention'
  serverId?: number
}
export const scopeKey = (s: Context) => `${s.actor}:${s.entity}:${s.outlet}`
export const sameContext = (a: Context, b: Context) => scopeKey(a) === scopeKey(b)
export const grantLifetime = 8 * 60 * 60 * 1000
export const maxLines = 80
export const maxQuantity = 999
export const maxTotal = 10_000_000_000

export const maxBarcodeLength = 80

export function isBarcode(value: unknown): value is string {
  // Keep the actual text, case and leading zeros. Reject spaces and control
  // characters instead of silently choosing a different identifier.
  return typeof value === 'string' && value.length > 0 && value.length <= maxBarcodeLength
    && !/[^!-~]/.test(value)
}

export function productByBarcode(catalog: readonly Product[], code: string): Product | undefined {
  if (!isBarcode(code)) return undefined
  const matches = catalog.filter((product) => product.codigo_barras === code)
  return matches.length === 1 && matches[0].activo ? matches[0] : undefined
}

export function normalizedProducts(rows: Product[]): Product[] {
  if (!Array.isArray(rows) || rows.length > 500) throw new Error('El catálogo no es válido.')
  const ids = new Set<number>()
  const barcodes = new Set<string>()
  for (const row of rows) {
    if (!positive(row.id) || ids.has(row.id) || !positive(row.revision) || (!positive(row.precio_centavos) || row.precio_centavos > 100_000_000)
      || typeof row.nombre !== 'string' || !row.nombre.trim() || typeof row.activo !== 'boolean') {
      throw new Error('El catálogo no es válido.')
    }
    if (row.codigo_barras !== undefined) {
      if (!isBarcode(row.codigo_barras) || barcodes.has(row.codigo_barras)) throw new Error('El catálogo no es válido.')
      barcodes.add(row.codigo_barras)
    }
    ids.add(row.id)
  }
  return rows.filter((row) => row.activo)
}

export function positive(value: number) {
  return Number.isSafeInteger(value) && value > 0
}

// Quote the in-memory cart without creating a sale or selecting a payment.
export function quoteCart(grant: Grant, quantities: ReadonlyMap<number, number>, now: number): { lines: Line[]; total: number } {
  if (now >= grant.expires || quantities.size < 1 || quantities.size > maxLines) throw new Error('La venta no se puede guardar.')
  const lines = [...quantities].sort(([a], [b]) => a - b).map(([product, quantity]) => {
    const item = grant.catalog.find((row) => row.id === product)
    if (!item?.activo || !positive(quantity) || quantity > maxQuantity) throw new Error('Revisá los productos y cantidades.')
    return { producto_id: product, revision: item.revision, cantidad: quantity, nombre: item.nombre, precio_centavos: item.precio_centavos }
  })
  const total = lines.reduce((sum, row) => sum + row.cantidad * row.precio_centavos, 0)
  if (!positive(total) || total > maxTotal) throw new Error('El importe total no es válido.')
  return { lines, total }
}

export function draft(grant: Grant, quantities: ReadonlyMap<number, number>, id: string, now: number, paymentMethod: PaymentMethod): Sale {
  if (!isPaymentMethod(paymentMethod)) throw new Error('Elegí cómo se pagó la venta.')
  const { lines, total } = quoteCart(grant, quantities, now)
  const { actor, entity, outlet } = grant
  const tillReportedAt = grant.tillTimeV1 === true ? capturedTillTime(now) : undefined
  return { id, key: scopeKey(grant), actor, entity, outlet, created: now, lines, total, paymentMethod, status: 'pending',
    ...(tillReportedAt ? { tillReportedAt } : {}) }
}

export function saleRequest(sale: Sale, tillTimeV1 = false) {
  return { id_solicitud: sale.id, actor_id: sale.actor,
    ...(sale.paymentMethod === undefined ? {} : { metodo_pago: sale.paymentMethod }),
    ...(tillTimeV1 === true && sale.tillReportedAt ? { hora_informada_caja_utc: sale.tillReportedAt } : {}),
    items: sale.lines.map(({ producto_id, revision, cantidad }) => ({ producto_id, revision, cantidad })) }
}

export function matchesPaymentMethod(sale: Sale, echoed: unknown) {
  return sale.paymentMethod === undefined ? echoed === undefined || echoed === 'efectivo' : echoed === sale.paymentMethod
}
