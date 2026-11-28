import type { PrintedReference } from './document-reference'

export type RemovedDraft = PrintedReference & {
  id: number
  revision: number
  removed_at: string
  issuer_cuit: string
}

export const restoreHandoffPrefix = '#restaurar-borrador='

export function parseRestoreHandoff(hash: string) {
  const match = /^#restaurar-borrador=([1-9][0-9]*):([1-9][0-9]*):([1-9][0-9]*)$/.exec(hash)
  if (!match || match[0] !== hash) return null
  const [entity, outlet, document] = match.slice(1).map(Number)
  return [entity, outlet, document].every(Number.isSafeInteger) ? { entity, outlet, document } : null
}
