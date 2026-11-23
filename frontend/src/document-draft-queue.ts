import { printedDocumentTypes } from './document-reference'

type DocumentType = keyof typeof printedDocumentTypes
export type DraftQueueRow = { id: number; issue_date: string; type: DocumentType; reference?: string }

const invalidQueue = () => new Error('No se pudo verificar la lista de borradores.')

export function draftQueueResponse(value: unknown, after: number) {
  const page = value as { drafts?: DraftQueueRow[]; status_filter?: string; next_id?: number } | null
  if (page?.status_filter !== 'draft' || !Array.isArray(page.drafts) || page.drafts.length > 30) throw invalidQueue()
  let last = after
  const drafts = page.drafts.map((row) => {
    if (!row || !Number.isSafeInteger(row.id) || row.id <= last ||
      typeof row.issue_date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(row.issue_date) ||
      !Object.hasOwn(printedDocumentTypes, row.type)) throw invalidQueue()
    if (row.reference !== undefined) {
      const type = printedDocumentTypes[row.type]
      if (typeof row.reference !== 'string' || !row.reference.startsWith(type + ' ') ||
        !/^(?:[A-Z] )?\d{5}-\d{8}$/.test(row.reference.slice(type.length + 1))) throw invalidQueue()
    }
    last = row.id
    return { id: row.id, issue_date: row.issue_date, type: row.type, reference: row.reference }
  })
  if (page.next_id !== undefined && (page.next_id !== last || drafts.length !== 30)) throw invalidQueue()
  return { drafts, nextID: page.next_id ?? 0 }
}
