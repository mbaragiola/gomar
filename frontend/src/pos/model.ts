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
}
export interface Grant extends Context {
  lease: string
  expires: number
  label: string
  catalog: Product[]
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

export function normalizedProducts(rows: Product[]): Product[] {
  if (!Array.isArray(rows) || rows.length > 500) throw new Error('El catálogo no es válido.')
  const ids = new Set<number>()
  for (const row of rows) {
    if (!positive(row.id) || ids.has(row.id) || !positive(row.revision) || (!positive(row.precio_centavos) || row.precio_centavos > 100_000_000)
      || typeof row.nombre !== 'string' || !row.nombre.trim() || typeof row.activo !== 'boolean') {
      throw new Error('El catálogo no es válido.')
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
  return { id, key: scopeKey(grant), actor, entity, outlet, created: now, lines, total, paymentMethod, status: 'pending' }
}

export function saleRequest(sale: Sale) {
  return { id_solicitud: sale.id, actor_id: sale.actor,
    ...(sale.paymentMethod === undefined ? {} : { metodo_pago: sale.paymentMethod }),
    items: sale.lines.map(({ producto_id, revision, cantidad }) => ({ producto_id, revision, cantidad })) }
}

export function matchesPaymentMethod(sale: Sale, echoed: unknown) {
  return sale.paymentMethod === undefined ? echoed === undefined || echoed === 'efectivo' : echoed === sale.paymentMethod
}
