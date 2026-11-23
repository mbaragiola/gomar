// This fragment identifies a browser view only. It is neither a credential
// nor an attachment link, and the server authorizes every requested ID.
export type DraftAttachmentHandoff = { entity: number; outlet: number; document: number }

export const draftHandoffPrefix = '#comprobante='
const positiveID = (id: number) => Number.isSafeInteger(id) && id > 0

export function parseDraftAttachmentHandoff(hash: string): DraftAttachmentHandoff | null {
  const match = /^#comprobante=([1-9][0-9]*):([1-9][0-9]*):([1-9][0-9]*)$/.exec(hash)
  if (!match || match[0] !== hash) return null
  const [entity, outlet, document] = match.slice(1).map(Number)
  return [entity, outlet, document].every(positiveID) ? { entity, outlet, document } : null
}

export function draftAttachmentHandoffHref(target: DraftAttachmentHandoff): string | undefined {
  return [target.entity, target.outlet, target.document].every(positiveID)
    ? `/${draftHandoffPrefix}${target.entity}:${target.outlet}:${target.document}`
    : undefined
}
