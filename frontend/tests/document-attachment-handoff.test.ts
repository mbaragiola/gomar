import { describe, expect, it } from 'vitest'
import { draftAttachmentHandoffHref, parseDraftAttachmentHandoff } from '../src/document-attachment-handoff'

describe('browser draft attachment selector', () => {
  it('round trips only canonical numeric IDs at the current origin root', () => {
    const ids = { entity: 1, outlet: 42, document: Number.MAX_SAFE_INTEGER }
    const local = '/#comprobante=1:42:9007199254740991'
    expect(draftAttachmentHandoffHref(ids)).toEqual(local)
    expect(parseDraftAttachmentHandoff(local.slice(1))).toEqual(ids)
  })

  it.each([
    '', '#otra-cosa', '#comprobante=01:2:3', '#comprobante=1:0:3', '#comprobante=-1:2:3',
    '#comprobante=1:2:+3', '#comprobante=1:2:3:4', '#comprobante=1:2:%33',
    '#comprobante=1:2:9007199254740992', '#comprobante=1:2:3&next=elsewhere',
    '#comprobante=1:2:3#comprobante=1:2:4', '#comprobante=1:2:3\n', '#comprobante=1:2:3/a.png',
  ])('does not treat %j as a document selector', hash => {
    expect(parseDraftAttachmentHandoff(hash)).toBeNull()
  })
})
