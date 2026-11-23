export type ReadAccess = { id: string; issued: number; expires: number }

function accessID(value: unknown): value is string {
  return typeof value === 'string' && /^[1-9]\d{0,18}$/.test(value) &&
    (value.length < 19 || value <= '9223372036854775807')
}

function earlierID(first: string, second: string): boolean {
  return first.length < second.length || first.length === second.length && first < second
}

function accessInstant(value: unknown): number {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(value)) return NaN
  const ms = Date.parse(value)
  return Number.isFinite(ms) && new Date(ms).toISOString() === value.replace('Z', '.000Z') ? ms : NaN
}

export function ownReadAccessPage(value: unknown, before = '') {
  const page = value as { access?: Record<string, unknown>[]; before_id?: unknown } | null
  const bad = () => new Error('invalid MCP read access list')
  if (!page || !Array.isArray(page.access) || page.access.length > 20 ||
      Object.keys(page).length !== (page.before_id === undefined ? 1 : 2)) throw bad()

  let last = before
  const access = page.access.map(row => {
    if (!row || typeof row !== 'object' || Object.keys(row).length !== 3 ||
      !accessID(row.id) || last && !earlierID(row.id, last)) throw bad()
    const issued = accessInstant(row.issued_at)
    const expires = accessInstant(row.expires_at)
    if (!Number.isFinite(issued) || !Number.isFinite(expires) || expires <= issued) throw bad()
    last = row.id
    return { id: row.id, issued, expires }
  })
  if (page.before_id !== undefined && (page.before_id !== last || access.length !== 20)) throw bad()
  return { access, cursor: typeof page.before_id === 'string' ? page.before_id : '' }
}
