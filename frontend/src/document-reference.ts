export const printedDocumentTypes = {
  invoice: 'Factura',
  credit_note: 'Nota de crédito',
  debit_note: 'Nota de débito',
} as const

export type PrintedReference = {
  document_type: keyof typeof printedDocumentTypes
  letter: string
  point_of_sale: number
  number: number
}

export type PrintedReferenceInput = {
  document_type: string
  letter: string
  point_of_sale: string
  number: string
}

export type PrintedReferenceMatch = {
  id: number
  issue_date: string
  status: 'draft' | 'reviewed'
}

export const blankPrintedReference = (): PrintedReferenceInput => ({
  document_type: '', letter: '-', point_of_sale: '', number: '',
})

export function parsePrintedReference(input: PrintedReferenceInput): PrintedReference | null {
  if (!Object.hasOwn(printedDocumentTypes, input.document_type) ||
    !/^(?:[A-Z])?$/.test(input.letter) ||
    !/^[0-9]{1,5}$/.test(input.point_of_sale) ||
    !/^[0-9]{1,8}$/.test(input.number)) return null
  const point = Number(input.point_of_sale)
  const number = Number(input.number)
  if (point < 1 || number < 1) return null
  return { document_type: input.document_type as PrintedReference['document_type'], letter: input.letter, point_of_sale: point, number }
}

export function printedReferenceLabel(reference: PrintedReference): string {
  const name = printedDocumentTypes[reference.document_type]
  return `${name}${reference.letter ? ' ' + reference.letter : ''} ${String(reference.point_of_sale).padStart(5, '0')}-${String(reference.number).padStart(8, '0')}`
}

export function printedReferenceResponse(value: unknown, reference: PrintedReference, after: number) {
  const page = value as { matches?: PrintedReferenceMatch[]; printed_reference?: PrintedReference; next_id?: number } | null
  const actual = page?.printed_reference
  if (!actual || actual.document_type !== reference.document_type || actual.letter !== reference.letter ||
    actual.point_of_sale !== reference.point_of_sale || actual.number !== reference.number ||
    !Array.isArray(page.matches) || page.matches.length > 30) throw new Error('No se pudo verificar la búsqueda por referencia.')
  let last = after
  const matches = page.matches.map((match) => {
    if (!match || !Number.isSafeInteger(match.id) || match.id <= last ||
      typeof match.issue_date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(match.issue_date) ||
      (match.status !== 'draft' && match.status !== 'reviewed')) throw new Error('No se pudo verificar la búsqueda por referencia.')
    last = match.id
    return { id: match.id, issue_date: match.issue_date, status: match.status }
  })
  if (page.next_id !== undefined && (page.next_id !== last || matches.length !== 30)) throw new Error('No se pudo verificar la búsqueda por referencia.')
  return { matches, nextID: page.next_id ?? 0 }
}
