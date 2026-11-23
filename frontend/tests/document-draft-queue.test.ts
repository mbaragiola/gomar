import { describe, expect, it } from 'vitest'
import { draftQueueResponse } from '../src/document-draft-queue'

describe('scoped document draft queue', () => {
  it('renders only acknowledged, safe metadata, including legitimate legacy zero references', () => {
    const row = { id: 3, issue_date: '2026-10-01', type: 'credit_note', reference: 'Nota de crédito A 00000-00000000' }
    expect(draftQueueResponse({ status_filter: 'draft', drafts: [{ ...row, supplier: 'private sentinel' }] }, 2))
      .toEqual({ drafts: [row], nextID: 0 })
    expect(draftQueueResponse({ status_filter: 'draft', drafts: [] }, 0)).toEqual({ drafts: [], nextID: 0 })
  })
  it('fails closed for unsupported filters, stale pages and unrecognized references', () => {
    const row = { id: 3, issue_date: '2026-10-01', type: 'invoice', reference: 'Factura 00001-00000001' }
    expect(() => draftQueueResponse([row], 0)).toThrow()
    expect(() => draftQueueResponse({ status_filter: 'reviewed', drafts: [row] }, 0)).toThrow()
    expect(() => draftQueueResponse({ status_filter: 'draft', drafts: [row] }, 3)).toThrow()
    expect(() => draftQueueResponse({ status_filter: 'draft', drafts: [row], next_id: 3 }, 0)).toThrow()
    expect(() => draftQueueResponse({ status_filter: 'draft', drafts: [{ ...row, reference: 'private sentinel' }] }, 0)).toThrow()
  })
})
