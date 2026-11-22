import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import McpProposalSessions from '../src/components/McpProposalSessions.vue'

const base = '/api/v1/entities/2/outlets/7'
const proposals = base + '/pos-rename-proposals'
const keyPath = '/api/v1/auth/mcp-proposal-sessions'
const expires_at = () => new Date(Date.now() + 500_000).toISOString()
const session = () => ({ id: 3, expires_at: expires_at(), actions: ['pos_product_rename'] })
const token = 'X'.repeat(43)
const digest = 'a'.repeat(64)
const pending = () => ({ id: 8, expires_at: expires_at(), status: 'pending' })
const rename = {
  product_id: 4, expected_revision: 5, name: '<b>Pan nuevo</b>', current_name: ' Pan original ',
  unchanged_price_minor: 123456, unchanged_active: false,
}
const result = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
const button = (panel: ReturnType<typeof mount>, name: string) => panel.findAll('button').find(b => b.text() === name)!
async function open(fetch: unknown, canRename = true) {
  vi.stubGlobal('fetch', fetch)
  const panel = mount(McpProposalSessions, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba', canRename } })
  await button(panel, 'Sesiones de propuestas').trigger('click')
  await flushPromises()
  return panel
}

afterEach(() => vi.unstubAllGlobals())

describe('Sesiones de propuestas', () => {
  it('requires an initially unchecked named action and never recovers the key after hiding it', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ ...session(), token }, 201))
      .mockResolvedValueOnce(result({ sessions: [session()] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
    const panel = await open(fetch)
    const checkbox = panel.get('input[type=checkbox]')
    expect((checkbox.element as HTMLInputElement).checked).toBe(false)
    expect(button(panel, 'Crear sesión de propuestas').attributes()).toHaveProperty('disabled')
    await button(panel, 'Crear sesión de propuestas').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    await checkbox.setValue(true)
    await button(panel, 'Crear sesión de propuestas').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(keyPath, expect.objectContaining({
      method: 'POST', body: JSON.stringify({ entity_id: 2, outlet_id: 7, actions: ['pos_product_rename'] }),
    }))
    expect(panel.text()).not.toContain(token)
    await button(panel, 'Mostrar clave una vez').trigger('click')
    expect(panel.text()).toContain(token)
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()
    expect(panel.text()).not.toContain(token)
    await button(panel, 'Sesiones de propuestas').trigger('click')
    await flushPromises()
    expect(panel.text()).not.toContain(token)
    expect(button(panel, 'Revocar sesión 3')).toBeTruthy()
    panel.unmount()
  })

  it('allows the same actor to revoke through management even with no rename capability', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [session()] }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(result({ sessions: [] }))
    const panel = await open(fetch, false)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(panel.find('input[type=checkbox]').exists()).toBe(false)
    await button(panel, 'Revocar sesión 3').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[1]).toEqual([keyPath + '/3', expect.objectContaining({ method: 'DELETE' })])
    expect(panel.text()).toContain('Sesión revocada')
    panel.unmount()
  })

  it.each(['approve', 'reject', 'stale'] as const)('checks exact rename and requires an explicit %s', async outcome => {
    const success = outcome === 'approve'
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), rename, digest }))
      .mockResolvedValueOnce(outcome === 'stale'
        ? result({ detail: token }, 409)
        : result({ ...pending(), status: success ? 'approved' : 'rejected', ...(success ? { product_id: 4, product_revision: 6 } : {}) }))
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
    const panel = await open(fetch)
    await button(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    expect(panel.get('[aria-label="Nombre actual"]').element.textContent).toBe(rename.current_name)
    expect(panel.get('[aria-label="Nombre propuesto"]').element.textContent).toBe(rename.name)
    expect(panel.find('pre b').exists()).toBe(false)
    expect(panel.text()).toContain('Centro de prueba · Producto 4 · Versión 5')
    expect(panel.text()).toContain('Precio sin cambios: ARS 1.234,56')
    expect(panel.text()).toContain('Estado sin cambios: Inactivo')
    const reject = outcome === 'reject'
    const begin = reject ? 'Rechazar propuesta' : 'Continuar con este cambio'
    await button(panel, begin).trigger('click')
    await button(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(3)
    await button(panel, begin).trigger('click')
    await button(panel, reject ? 'Sí, rechazar' : 'Confirmar cambio de nombre').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[3]).toEqual([proposals + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: reject ? 'reject' : 'approve', digest, confirm: true }),
    })])
    expect(fetch.mock.calls.filter(call => call[1].method === 'POST')).toHaveLength(1)
    expect(panel.text()).toContain(success ? 'Nombre del producto actualizado' : reject ? 'No se modificó el producto' : 'No se cambió el nombre')
    expect(panel.text()).not.toContain(token)
    expect(panel.find('pre').exists()).toBe(false)
    panel.unmount()
  })
})

describe('Propuestas para revisar comprobantes', () => {
  const reviewPath = base + '/document-review-proposals'
  const review = {
    document_id: 31, expected_revision: 2, type: 'invoice', total_minor: 5025, currency: 'USD', reason: '<b>Nota de prueba</b>\nSegunda línea',
  }
  async function openReview(fetch: unknown, canRename: boolean) {
    vi.stubGlobal('fetch', fetch)
    const panel = mount(McpProposalSessions, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba', canRename, canReviewDocuments: true } })
    await button(panel, 'Sesiones de propuestas').trigger('click')
    await flushPromises()
    return panel
  }

  it('offers two independent unchecked actions, but issues only the one selected', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ ...session(), actions: ['document_draft_review'], token }, 201))
    const panel = await openReview(fetch, true)
    const [pos, doc] = panel.findAll<HTMLInputElement>('input[type=checkbox]')
    expect([pos.element.checked, doc.element.checked]).toEqual([false, false])
    await pos.setValue(true)
    expect(panel.text()).toContain('--proposal-session-action=pos_product_rename')
    await doc.setValue(true)
    expect(panel.text()).not.toContain('--proposal-session-action=pos_product_rename')
    expect(panel.text()).toContain('--proposal-session-action=document_draft_review')
    expect([pos.element.checked, doc.element.checked]).toEqual([false, true])
    await button(panel, 'Crear sesión de propuestas').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(keyPath, expect.objectContaining({
      method: 'POST', body: JSON.stringify({ entity_id: 2, outlet_id: 7, actions: ['document_draft_review'] }),
    }))
    expect(panel.text()).not.toContain(token)
    expect(panel.text()).toContain('Marcar un comprobante como revisado')
    expect(panel.text()).toContain('--proposal-session-action=document_draft_review')
    panel.unmount()
  })

  it('can approve a legacy revision zero and checks the new revision is one', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), review: { ...review, expected_revision: 0 }, digest }))
      .mockResolvedValueOnce(result({ ...pending(), status: 'approved', document_id: 31, document_revision: 1 }))
    const panel = await openReview(fetch, false)
    await button(panel, 'Ver propuesta 8 de revisión').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('Comprobante 31 · Versión 0')
    await button(panel, 'Continuar con la revisión').trigger('click')
    await button(panel, 'Marcar como revisado').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('Comprobante marcado como revisado')
    panel.unmount()
  })

  it.each([null, undefined, -1])('refuses invalid revision %s', async expected_revision => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), review: { ...review, expected_revision }, digest }))
    const panel = await openReview(fetch, false)
    await button(panel, 'Ver propuesta 8 de revisión').trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toBe('No pude abrir la propuesta.')
    expect(button(panel, 'Continuar con la revisión')).toBeUndefined()
    panel.unmount()
  })

  it.each(['approve', 'reject', 'stale'] as const)('shows the exact safe preview and handles %s only for the document', async outcome => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), review, digest }))
      .mockResolvedValueOnce(outcome === 'stale'
        ? result({ detail: token }, 409)
        : result({ ...pending(), status: outcome === 'approve' ? 'approved' : 'rejected',
          ...(outcome === 'approve' ? { document_id: 31, document_revision: 3 } : {}) }))
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
    const panel = await openReview(fetch, true)
    await button(panel, 'Ver propuesta 8 de revisión').trigger('click')
    await flushPromises()
    expect(panel.get('[aria-label="Motivo de la revisión"]').element.textContent).toBe(review.reason)
    expect(panel.find('pre b').exists()).toBe(false)
    expect(panel.text()).toContain('Centro de prueba · Comprobante 31 · Versión 2')
    expect(panel.text()).toContain('Factura · Total: USD 50,25')
    expect(panel.text()).toContain('No se registra una deuda ni un pago')
    const reject = outcome === 'reject'
    const begin = reject ? 'Rechazar propuesta' : 'Continuar con la revisión'
    await button(panel, begin).trigger('click')
    await button(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(4)
    await button(panel, begin).trigger('click')
    await button(panel, reject ? 'Sí, rechazar' : 'Marcar como revisado').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[4]).toEqual([reviewPath + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: reject ? 'reject' : 'approve', digest, confirm: true }),
    })])
    expect(fetch.mock.calls.filter(call => call[1].method === 'POST')).toHaveLength(1)
    expect(panel.text()).toContain(reject ? 'No se modificó el comprobante' : outcome === 'stale' ? 'No se marcó como revisado' : 'Comprobante marcado como revisado')
    expect(panel.text()).not.toContain(token)
    expect(button(panel, 'Ver propuesta 8')).toBeTruthy()
    panel.unmount()
  })
})

describe('Borradores vacíos de conciliación', () => {
  const draftPath = base + '/reconciliation-draft-proposals'
  const action = 'reconciliation_draft_create'
  async function openDraft(fetch: unknown, otherActions = false) {
    vi.stubGlobal('fetch', fetch)
    const panel = mount(McpProposalSessions, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba', canRename: otherActions, canReviewDocuments: otherActions, canCreateReconciliationDraft: true } })
    await button(panel, 'Sesiones de propuestas').trigger('click')
    await flushPromises()
    return panel
  }

  it('starts unchecked and issues only the explicitly chosen reconciliation action', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ ...session(), actions: [action], token }, 201))
    const panel = await openDraft(fetch, true)
    const [pos, review, draft] = panel.findAll<HTMLInputElement>('input[type=checkbox]')
    expect([pos, review, draft].map(option => option.element.checked)).toEqual([false, false, false])
    expect(button(panel, 'Crear sesión de propuestas').attributes()).toHaveProperty('disabled')
    await pos.setValue(true)
    await review.setValue(true)
    await draft.setValue(true)
    expect([pos, review, draft].map(option => option.element.checked)).toEqual([false, false, true])
    expect(panel.text()).toContain('--proposal-session-action=reconciliation_draft_create')
    await button(panel, 'Crear sesión de propuestas').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('--proposal-session-action=reconciliation_draft_create')
    expect(panel.text()).not.toContain(token)
    expect(fetch).toHaveBeenLastCalledWith(keyPath, expect.objectContaining({
      method: 'POST', body: JSON.stringify({ entity_id: 2, outlet_id: 7, actions: [action] }),
    }))
    panel.unmount()
  })

  it.each([
    { match_reference: false, outcome: 'approve' },
    { match_reference: true, outcome: 'reject' },
    { match_reference: false, outcome: 'stale' },
  ])('shows exact match-reference $match_reference and handles $outcome deliberately', async ({ match_reference, outcome }) => {
    const approved = outcome === 'approve'
    const rejected = outcome === 'reject'
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), draft: { match_reference }, digest }))
      .mockResolvedValueOnce(outcome === 'stale'
        ? result({ detail: token }, 409)
        : result({ ...pending(), status: approved ? 'approved' : 'rejected', ...(approved ? { run_id: 42 } : {}) }))
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
    const panel = await openDraft(fetch)
    await button(panel, 'Ver propuesta 8 de conciliación').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('Local: Centro de prueba')
    expect(panel.text()).toContain('Usar la referencia para buscar coincidencias: ' + (match_reference ? 'Sí' : 'No'))
    expect(panel.text()).toContain('No se importan archivos ni movimientos')
    const begin = rejected ? 'Rechazar propuesta' : 'Continuar con el borrador'
    await button(panel, begin).trigger('click')
    await button(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(3)
    await button(panel, begin).trigger('click')
    await button(panel, rejected ? 'Sí, rechazar' : 'Crear borrador vacío').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[3]).toEqual([draftPath + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: rejected ? 'reject' : 'approve', digest, confirm: true }),
    })])
    expect(fetch.mock.calls.filter(call => call[1].method === 'POST')).toHaveLength(1)
    expect(panel.text()).toContain(approved ? 'Borrador vacío de conciliación creado' : rejected ? 'No se creó un borrador' : 'No se creó el borrador')
    expect(panel.text()).not.toContain(token)
    panel.unmount()
  })

  it.each([null, undefined, 'false'])('refuses a non-boolean match-reference %s', async match_reference => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), draft: { match_reference }, digest }))
    const panel = await openDraft(fetch)
    await button(panel, 'Ver propuesta 8 de conciliación').trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toBe('No pude abrir la propuesta.')
    expect(button(panel, 'Continuar con el borrador')).toBeUndefined()
    panel.unmount()
  })
})

describe('Editar fechas e importes de un borrador', () => {
  const editPath = base + '/document-edit-proposals'
  const edit = {
    document_id: 14, expected_revision: 0, reason: '<b>Motivo de prueba</b>\nSin HTML.', currency: 'USD',
    before: { issue_date: '2026-11-01', due_date: null, net_minor: null, tax_minor: null, total_minor: 120 },
    after: { issue_date: '2026-11-02', due_date: '2026-11-23', net_minor: 0, tax_minor: 190, total_minor: 190 },
  }
  async function openEdit(fetch: unknown) {
    vi.stubGlobal('fetch', fetch)
    const panel = mount(McpProposalSessions, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba', canRename: false, canEditDocuments: true } })
    await button(panel, 'Sesiones de propuestas').trigger('click')
    await flushPromises()
    return panel
  }

  it('starts unchecked and issues only the edit action with its exact flag', async () => {
    const action = 'document_draft_edit'
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ ...session(), actions: [action], token }, 201))
    const panel = await openEdit(fetch)
    const checkbox = panel.get<HTMLInputElement>('input[type=checkbox]')
    expect(checkbox.element.checked).toBe(false)
    expect(button(panel, 'Crear sesión de propuestas').attributes()).toHaveProperty('disabled')
    await checkbox.setValue(true)
    expect(panel.text()).toContain('--proposal-session-action=document_draft_edit')
    await button(panel, 'Crear sesión de propuestas').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(keyPath, expect.objectContaining({
      method: 'POST', body: JSON.stringify({ entity_id: 2, outlet_id: 7, actions: [action] }),
    }))
    expect(panel.text()).toContain('--proposal-session-action=document_draft_edit')
    expect(panel.text()).not.toContain(token)
    panel.unmount()
  })

  it.each(['approve', 'reject', 'stale'] as const)('shows exact nulls, zero, and both dates before %s', async outcome => {
    const approved = outcome === 'approve'
    const rejected = outcome === 'reject'
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), edit, digest }))
      .mockResolvedValueOnce(outcome === 'stale'
        ? result({ detail: token }, 409)
        : result({ ...pending(), status: approved ? 'approved' : 'rejected', ...(approved ? { document_id: 14, document_revision: 1 } : {}) }))
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
    const panel = await openEdit(fetch)
    await button(panel, 'Ver propuesta 8 de edición').trigger('click')
    await flushPromises()
    const before = panel.get('[aria-label="Antes de la edición"]')
    const after = panel.get('[aria-label="Después de la edición"]')
    expect(before.findAll('dd').map(e => e.text())).toEqual(['2026-11-01', 'No informado', 'No informado', 'No informado', 'USD 1,20'])
    expect(after.findAll('dd').map(e => e.text())).toEqual(['2026-11-02', '2026-11-23', '0,00', '1,90', 'USD 1,90'])
    expect(panel.get('[aria-label="Motivo de la edición"]').element.textContent).toBe(edit.reason)
    expect(panel.find('pre b').exists()).toBe(false)
    expect(panel.text()).toContain('Centro de prueba · Comprobante 14 · Versión 0')
    expect(panel.text()).toContain('El comprobante sigue en borrador')
    const begin = rejected ? 'Rechazar propuesta' : 'Continuar con la edición'
    await button(panel, begin).trigger('click')
    await button(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(3)
    await button(panel, begin).trigger('click')
    await button(panel, rejected ? 'Sí, rechazar' : 'Guardar fechas e importes').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[3]).toEqual([editPath + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: rejected ? 'reject' : 'approve', digest, confirm: true }),
    })])
    expect(fetch.mock.calls.filter(call => call[1].method === 'POST')).toHaveLength(1)
    expect(panel.text()).toContain(approved ? 'Fechas e importes del borrador guardados' : rejected ? 'No se modificó el borrador' : 'No se guardó la edición')
    expect(panel.text()).not.toContain(token)
    panel.unmount()
  })

  it('does not confuse absent values with explicit nulls in a pending preview', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), edit: { ...edit, after: { ...edit.after, tax_minor: undefined } }, digest }))
    const panel = await openEdit(fetch)
    await button(panel, 'Ver propuesta 8 de edición').trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toBe('No pude abrir la propuesta.')
    expect(button(panel, 'Continuar con la edición')).toBeUndefined()
    panel.unmount()
  })
})
