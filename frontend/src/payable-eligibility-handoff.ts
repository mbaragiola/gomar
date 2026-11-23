import { parsePrintedReference } from './document-reference'

export type PayableEligibilityTarget = { entity: number; outlet: number; document: number }
const prefix = '#alta-cuenta'

export function isPayableEligibilityHandoff(path: string, hash: string): boolean {
  if (path !== '/') return false
  // Reserve even an encoded prefix with an invalid suffix. This tolerant
  // detection never authorizes a target; the strict parser below does.
  const apparent = hash.replace(/%([0-9a-f]{2})/gi, (_, hex: string) => String.fromCharCode(Number.parseInt(hex, 16)))
  return apparent.toLowerCase().startsWith(prefix)
}

export function parsePayableEligibilityHandoff(hash: string, search: string): PayableEligibilityTarget | null {
  if (search) return null
  const match = /^#alta-cuenta=([1-9][0-9]{0,15}):([1-9][0-9]{0,15}):([1-9][0-9]{0,15})$/.exec(hash)
  if (!match || match[0] !== hash) return null
  const [entity, outlet, document] = match.slice(1).map(Number)
  return [entity, outlet, document].every(Number.isSafeInteger) ? { entity, outlet, document } : null
}

export function payableEligibilityReference(value: unknown, target: PayableEligibilityTarget) {
  if (value === null || typeof value !== 'object') return null
  const view = value as { document_id?: unknown; printed_reference?: unknown }
  if (Object.keys(view).length !== 2 || view.document_id !== target.document ||
    view.printed_reference === null || typeof view.printed_reference !== 'object') return null
  const ref = view.printed_reference as { document_type?: unknown; letter?: unknown; point_of_sale?: unknown; number?: unknown }
  if (Object.keys(ref).length !== 4 || ref.document_type !== 'invoice' || typeof ref.letter !== 'string' ||
    typeof ref.point_of_sale !== 'number' || !Number.isInteger(ref.point_of_sale) ||
    typeof ref.number !== 'number' || !Number.isInteger(ref.number)) return null
  return parsePrintedReference({
    document_type: ref.document_type,
    letter: ref.letter,
    point_of_sale: String(ref.point_of_sale),
    number: String(ref.number),
  })
}
