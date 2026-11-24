export interface ReceiptClock {
  registeredAt: string
  tillReportedAt?: string
  status: 'not_reported' | 'unverified' | 'review'
}

export interface SaleTimeTarget {
  entity: number
  outlet: number
  id: string
}

const reference = '[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
const handoffPattern = new RegExp('^#entity=([1-9][0-9]*)&outlet=([1-9][0-9]*)&venta=(' + reference + ')$')
const saleTimeMarker = /(?:^|[?&#;])\s*venta\s*(?=[=?&#;]|$)/i
const utcPattern = /^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$/

export function capturedTillTime(now: number): string | undefined {
  const date = new Date(now)
  const year = date.getUTCFullYear()
  return year >= 1 && year <= 9999 ? date.toISOString() : undefined
}

function looksLikeSaleTime(value: string): boolean {
  // Decode permissively for reservation only, including malformed escapes in
  // other fields. A read-only link never falls back to the register on error.
  const decoded = new URLSearchParams('route=' + value.replaceAll('&', '%26')).get('route') ?? ''
  return saleTimeMarker.test(decoded)
}

export function saleTimeRoute(hash: string, search: string, emptyQuery: boolean): { target?: SaleTimeTarget } | undefined {
  if (!looksLikeSaleTime(hash) && !looksLikeSaleTime(search)) return
  return { target: !search && !emptyQuery ? saleTimeTarget(hash) : undefined }
}

export function saleTimeTarget(hash: string): SaleTimeTarget | undefined {
  const match = handoffPattern.exec(hash)
  if (!match || match[0] !== hash) return
  const entity = Number(match[1])
  const outlet = Number(match[2])
  if (!Number.isSafeInteger(entity) || !Number.isSafeInteger(outlet)) return
  return { entity, outlet, id: match[3] }
}

export function saleTimeHandoff(target: SaleTimeTarget): string {
  return `/pos#entity=${target.entity}&outlet=${target.outlet}&venta=${target.id}`
}

function validUTC(value: unknown): value is string {
  if (typeof value !== 'string' || !utcPattern.test(value)) return false
  const date = new Date(value)
  return Number.isFinite(date.valueOf()) && date.getUTCFullYear() > 0 && date.toISOString().slice(0, 19) === value.slice(0, 19)
}

function clockFrom(registeredAt: unknown, tillReportedAt: unknown, status: unknown): ReceiptClock | undefined {
  if (!validUTC(registeredAt)) return
  if (status === 'not_reported' && tillReportedAt === undefined) return { registeredAt, status }
  if (validUTC(tillReportedAt) && (status === 'unverified' || status === 'review')) {
    return { registeredAt, tillReportedAt, status }
  }
}

interface SaleAcknowledgmentClock {
  creado_en?: unknown
  hora_informada_caja_utc?: unknown
  estado_hora_caja?: unknown
}

export function receiptClock(response: SaleAcknowledgmentClock): ReceiptClock | undefined {
  return clockFrom(response.creado_en, response.hora_informada_caja_utc, response.estado_hora_caja ?? 'not_reported')
}

export function lookupClock(response: unknown, target: SaleTimeTarget): ReceiptClock | undefined {
  if (!response || typeof response !== 'object') return
  const result = response as Record<string, unknown>
  if (result.request_id !== target.id || result.handoff_url !== saleTimeHandoff(target)) return
  return clockFrom(result.registered_at_utc, result.till_reported_at_utc, result.till_time_status)
}
