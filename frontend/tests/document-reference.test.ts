import { describe, expect, it } from 'vitest'
import { blankPrintedReference, parsePrintedReference, printedReferenceLabel, printedReferenceResponse } from '../src/document-reference'

describe('exact printed reference search', () => {
  const input = { document_type: 'credit_note', letter: '', point_of_sale: '00012', number: '00000345' }
  const reference = { document_type: 'credit_note' as const, letter: '', point_of_sale: 12, number: 345 }
  it('only accepts complete native fiscal fields and formats the printed zeroes', () => {
    expect(parsePrintedReference(blankPrintedReference())).toBeNull()
    expect(parsePrintedReference(input)).toEqual(reference)
    expect(printedReferenceLabel(reference)).toBe('Nota de crédito 00012-00000345')
    expect(parsePrintedReference({ ...input, letter: '-' })).toBeNull()
    expect(parsePrintedReference({ ...input, letter: 'a' })).toBeNull()
    expect(parsePrintedReference({ ...input, number: '00000000' })).toBeNull()
    expect(parsePrintedReference({ ...input, point_of_sale: '100000' })).toBeNull()
    expect(parsePrintedReference({ ...input, number: '100000000' })).toBeNull()
  })

  it('keeps multiple matches and rejects a legacy, mismatched or repeated-page response', () => {
    const matches = [
      { id: 5, issue_date: '2026-04-03', status: 'draft' },
      { id: 6, issue_date: '2026-04-03', status: 'reviewed' },
    ]
    expect(printedReferenceResponse({ matches, printed_reference: reference }, reference, 4)).toEqual({ matches, nextID: 0 })
    expect(printedReferenceResponse({ matches: [], printed_reference: reference }, reference, 0)).toEqual({ matches: [], nextID: 0 })
    expect(() => printedReferenceResponse(matches, reference, 0)).toThrow()
    expect(() => printedReferenceResponse({ matches, printed_reference: { ...reference, letter: null } }, reference, 0)).toThrow()
    expect(() => printedReferenceResponse({ matches, printed_reference: reference }, reference, 5)).toThrow()
    expect(() => printedReferenceResponse({ matches, printed_reference: reference, next_id: 6 }, reference, 4)).toThrow()
  })
})
