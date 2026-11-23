export type ReconciliationCloseTarget = { entity: number; outlet: number; run: number }

export function isReconciliationCloseHandoff(path: string, hash: string): boolean {
  if (path !== '/') return false
  try {
    return decodeURIComponent(hash).toLowerCase().startsWith('#conciliacion')
  } catch {
    return hash.toLowerCase().startsWith('#conciliacion')
  }
}

export function parseReconciliationCloseHandoff(hash: string, search: string): ReconciliationCloseTarget | null {
  if (search) return null
  const match = /^#conciliacion=([1-9][0-9]{0,15}):([1-9][0-9]{0,15}):([1-9][0-9]{0,15})$/.exec(hash)
  if (!match || match[0] !== hash) return null
  const [entity, outlet, run] = match.slice(1).map(Number)
  return [entity, outlet, run].every(Number.isSafeInteger) ? { entity, outlet, run } : null
}
