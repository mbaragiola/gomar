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

describe('Ajustes de stock', () => {
  const waste = { product_id: 21, product_name: '<b>Pan</b>', revision: 3, stock_minimum: 2, latest_movement: 8, before: 7, after: 4, delta: -3, quantity: 3 }
  const count = { ...waste, quantity: 0, before: 7, after: 0, delta: -7 }
  const issuePath = '/api/v1/auth/mcp-proposal-sessions'
  const wastePath = base + '/stock-waste-proposals'
  const countPath = base + '/stock-physical-count-proposals'
  async function openStock(fetch: unknown) {
    vi.stubGlobal('fetch', fetch)
    const panel = mount(McpProposalSessions, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba', canRename: false, canAdjustStock: true } })
    await button(panel, 'Sesiones de propuestas').trigger('click')
    await flushPromises()
    return panel
  }

  it('offers two unchecked actions and issues only the selected stock action', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ ...session(), actions: ['stock_physical_count'], token }, 201))
    const panel = await openStock(fetch)
    const [merma, conteo] = panel.findAll<HTMLInputElement>('input[type=checkbox]')
    expect([merma.element.checked, conteo.element.checked]).toEqual([false, false])
    await merma.setValue(true)
    expect(panel.text()).toContain('--proposal-session-action=stock_waste_record')
    await conteo.setValue(true)
    expect([merma.element.checked, conteo.element.checked]).toEqual([false, true])
    await button(panel, 'Crear sesión de propuestas').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('--proposal-session-action=stock_physical_count')
    expect(fetch).toHaveBeenLastCalledWith(issuePath, expect.objectContaining({
      method: 'POST', body: JSON.stringify({ entity_id: 2, outlet_id: 7, actions: ['stock_physical_count'] }),
    }))
    expect(panel.text()).not.toContain(token)
    panel.unmount()
  })

  it.each([
    { physical: true, outcome: 'approve' },
    { physical: false, outcome: 'approve' },
    { physical: false, outcome: 'reject' },
    { physical: true, outcome: 'stale' },
  ])('shows exact product and delta; physical=$physical, outcome=$outcome', async ({ physical, outcome }) => {
    const approved = outcome === 'approve'
    const rejected = outcome === 'reject'
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), adjustment: physical ? count : waste, digest }))
      .mockResolvedValueOnce(outcome === 'stale'
        ? result({ detail: token }, 409)
        : result({ ...pending(), status: approved ? 'approved' : 'rejected', ...(approved ? { movement_id: 23 } : {}) }))
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
    const panel = await openStock(fetch)
    await button(panel, 'Ver propuesta 8 de ' + (physical ? 'conteo' : 'merma')).trigger('click')
    await flushPromises()
    const preview = panel.get('[aria-label="Vista previa de la propuesta"]')
    expect(preview.text()).toContain('Centro de prueba')
    expect(preview.text()).toContain('Producto: <b>Pan</b> (21)')
    expect(preview.find('b').exists()).toBe(false)
    expect(preview.text()).toContain(physical ? 'Unidades contadas: 0' : 'Unidades de merma: 3')
    expect(preview.text()).toContain(physical ? 'Saldo: 7 → 0' : 'Saldo: 7 → 4')
    expect(preview.text()).toContain(physical ? 'Cambio: -7' : 'Cambio: -3')
    const begin = rejected ? 'Rechazar propuesta' : physical ? 'Continuar con el conteo' : 'Continuar con la merma'
    await button(panel, begin).trigger('click')
    await button(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(4)
    await button(panel, begin).trigger('click')
    await button(panel, rejected ? 'Sí, rechazar' : physical ? 'Registrar conteo físico' : 'Registrar merma').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[4]).toEqual([(physical ? countPath : wastePath) + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: rejected ? 'reject' : 'approve', digest, confirm: true }),
    })])
    expect(fetch.mock.calls.filter(call => call[1].method === 'POST')).toHaveLength(1)
    expect(panel.text()).toContain(approved ? physical ? 'Conteo físico registrado' : 'Merma registrada' : rejected ? 'El stock no cambió' : 'El stock o la propuesta cambiaron')
    expect(panel.text()).not.toContain(token)
    panel.unmount()
  })

  it.each([
    { physical: false, adjustment: { ...waste, quantity: 0, after: 7, delta: 0 } },
    { physical: true, adjustment: { ...count, quantity: null } },
    { physical: true, adjustment: { ...count, delta: 7 } },
  ])('refuses mismatched or invalid pending stock previews', async ({ physical, adjustment }) => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), adjustment, digest }))
    const panel = await openStock(fetch)
    await button(panel, 'Ver propuesta 8 de ' + (physical ? 'conteo' : 'merma')).trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toBe('No pude abrir la propuesta.')
    expect(panel.find('[aria-label="Vista previa de la propuesta"]').exists()).toBe(false)
    panel.unmount()
  })
})

describe('Precio y disponibilidad de caja', () => {
  const price = {
    product_id: 17, expected_revision: 5, name: '<b>Pan</b>', current_price_minor: 200, price_minor: 350, current_active: true, active: true,
  }
  const availability = { ...price, price_minor: 200, active: false }
  async function openCatalog(fetch: unknown) {
    vi.stubGlobal('fetch', fetch)
    const panel = mount(McpProposalSessions, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba', canRename: false, canChangeCatalog: true } })
    await button(panel, 'Sesiones de propuestas').trigger('click')
    await flushPromises()
    return panel
  }

  it('requires one explicit named action and shows the exact per-action CLI switch', async () => {
    const action = 'pos_product_availability_change'
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ ...session(), actions: [action], token }, 201))
    const panel = await openCatalog(fetch)
    const [priceChoice, availabilityChoice] = panel.findAll<HTMLInputElement>('input[type=checkbox]')
    expect([priceChoice.element.checked, availabilityChoice.element.checked]).toEqual([false, false])
    expect(button(panel, 'Crear sesión de propuestas').attributes()).toHaveProperty('disabled')
    await priceChoice.setValue(true)
    expect(panel.text()).toContain('--proposal-session-action=pos_product_price_change')
    await availabilityChoice.setValue(true)
    expect([priceChoice.element.checked, availabilityChoice.element.checked]).toEqual([false, true])
    await button(panel, 'Crear sesión de propuestas').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(keyPath, expect.objectContaining({
      method: 'POST', body: JSON.stringify({ entity_id: 2, outlet_id: 7, actions: [action] }),
    }))
    expect(panel.text()).toContain('--proposal-session-action=pos_product_availability_change')
    expect(panel.text()).not.toContain(token)
    panel.unmount()
  })

  it.each([
    { changePrice: true, outcome: 'approve' },
    { changePrice: false, outcome: 'approve' },
    { changePrice: true, outcome: 'reject' },
    { changePrice: false, outcome: 'stale' },
  ])('shows exact before and after, price=$changePrice, outcome=$outcome', async ({ changePrice, outcome }) => {
    const approved = outcome === 'approve'
    const rejected = outcome === 'reject'
    const kind = changePrice ? 'precio' : 'disponibilidad'
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), change: changePrice ? price : availability, digest }))
      .mockResolvedValueOnce(outcome === 'stale' ? result({ detail: token }, 409)
        : result({ ...pending(), status: approved ? 'approved' : 'rejected', ...(approved ? { product_id: 17, product_revision: 6 } : {}) }))
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
    const panel = await openCatalog(fetch)
    await button(panel, 'Ver propuesta 8 de ' + kind).trigger('click')
    await flushPromises()
    const preview = panel.get('[aria-label="Vista previa de la propuesta"]')
    expect(preview.text()).toContain('Centro de prueba · Producto 17 · Versión 5')
    expect(preview.text()).toContain('Nombre sin cambios: <b>Pan</b>')
    expect(preview.find('b').exists()).toBe(false)
    expect(preview.text()).toContain(changePrice ? 'Precio actual: ARS 2,00' : 'Disponibilidad actual: Activo')
    expect(preview.text()).toContain(changePrice ? 'Precio propuesto: ARS 3,50' : 'Disponibilidad propuesta: Inactivo')
    expect(preview.text()).toContain(changePrice ? 'Disponibilidad sin cambios: Activo' : 'Precio sin cambios: ARS 2,00')
    const proceed = rejected ? 'Rechazar propuesta' : changePrice ? 'Continuar con el precio' : 'Continuar con la disponibilidad'
    await button(panel, proceed).trigger('click')
    await button(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(4)
    await button(panel, proceed).trigger('click')
    await button(panel, rejected ? 'Sí, rechazar' : changePrice ? 'Confirmar nuevo precio' : 'Confirmar disponibilidad').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[4]).toEqual([base + (changePrice ? '/pos-price-proposals' : '/pos-availability-proposals') + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: rejected ? 'reject' : 'approve', digest, confirm: true }),
    })])
    expect(fetch.mock.calls.filter(call => call[1].method === 'POST')).toHaveLength(1)
    expect(panel.text()).toContain(approved ? changePrice ? 'Precio actualizado' : 'Disponibilidad actualizada'
      : rejected ? 'El producto no cambió' : 'No se cambió la disponibilidad')
    expect(panel.text()).not.toContain(token)
    panel.unmount()
  })

  it.each([
    { change: { ...price, active: false }, kind: 'precio' },
    { change: { ...price, price_minor: 2.5 }, kind: 'precio' },
    { change: { ...availability, active: undefined }, kind: 'disponibilidad' },
  ])('refuses incomplete or mixed changes in $kind', async ({ change, kind }) => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ sessions: [] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), change, digest }))
    const panel = await openCatalog(fetch)
    await button(panel, 'Ver propuesta 8 de ' + kind).trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toBe('No pude abrir la propuesta.')
    panel.unmount()
  })
})

describe('Confirmación de una reserva', () => {
  const path = base + '/reservation-confirm-proposals'
  const reference = '4'.repeat(32)
  const confirmation = {
    reservation_reference: reference, start_utc: '2027-09-03T01:30:00Z',
    start_local: '2027-09-02T22:30:00-03:00', timezone: 'America/Argentina/Buenos_Aires',
    party_size: 3, status: 'pending', table_label: 'Mesa de prueba', table_capacity: 4,
  }
  async function reservationPanel(fetch: unknown, allowed = true) {
    vi.stubGlobal('fetch', fetch)
    const panel = mount(McpProposalSessions, {
      props: { entity: 2, outlet: 7, outletName: 'Centro de prueba', canRename: false, canConfirmReservations: allowed },
    })
    await button(panel, 'Sesiones de propuestas').trigger('click')
    await flushPromises()
    return panel
  }
  function fakeFetch(reply: Response, preview = confirmation) {
    return vi.fn((url: string) => {
      if (url.endsWith('/proposal-sessions')) return Promise.resolve(result({ sessions: [] }))
      if (url === path) return Promise.resolve(result({ proposals: [pending()] }))
      if (url === path + '/8') return Promise.resolve(result({
        ...pending(), digest, confirmation: { ...preview, guest_name: 'name sentinel', guest_phone: 'phone sentinel' },
      }))
      if (url === path + '/8/decision') return Promise.resolve(reply)
      throw Error('unexpected test request')
    })
  }

  it('previews the exact scoped reservation, with a separate confirmation and no guest data', async () => {
    const fetch = fakeFetch(result({ ...pending(), status: 'approved', reservation_reference: reference }))
    const panel = await reservationPanel(fetch)
    await panel.get('input[type=checkbox]').setValue(true)
    expect(panel.text()).toContain('--proposal-session-action=reservation_confirm')
    await button(panel, 'Ver propuesta 8 de reserva').trigger('click')
    await flushPromises()
    for (const text of ['Centro de prueba', reference, '2027-09-02 · 22:30', 'Personas: 3', 'Mesa de prueba', 'Capacidad: 4']) {
      expect(panel.text()).toContain(text)
    }
    expect(panel.text()).not.toContain('sentinel')
    await button(panel, 'Continuar con la reserva').trigger('click')
    expect(fetch.mock.calls.some(([url]) => url.endsWith('/decision'))).toBe(false)
    await button(panel, 'Confirmar reserva').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(path + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: 'approve', digest, confirm: true }),
    }))
    expect(panel.text()).toContain('Reserva confirmada.')
    panel.unmount()
  })

  it.each([409, 410])('does not retry a stale or expired decision (%s) even if it still lists as pending', async status => {
    const fetch = fakeFetch(result({}, status))
    const panel = await reservationPanel(fetch)
    await button(panel, 'Ver propuesta 8 de reserva').trigger('click')
    await flushPromises()
    await button(panel, 'Continuar con la reserva').trigger('click')
    await button(panel, 'Confirmar reserva').trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toMatch(/No se confirmó|venció/)
    await button(panel, 'Ver propuesta 8 de reserva').trigger('click')
    await flushPromises()
    expect(button(panel, 'Continuar con la reserva').attributes()).toHaveProperty('disabled')
    expect(fetch.mock.calls.filter(([url]) => url.endsWith('/decision'))).toHaveLength(1)
    panel.unmount()
  })

  it('rejects without confirming a booking and rejects a mismatched approval receipt', async () => {
    for (const decision of ['reject', 'approve']) {
      const fetch = fakeFetch(result(decision === 'reject'
        ? { ...pending(), status: 'rejected' }
        : { ...pending(), status: 'approved', reservation_reference: 'f'.repeat(32) }))
      const panel = await reservationPanel(fetch)
      await button(panel, 'Ver propuesta 8 de reserva').trigger('click')
      await flushPromises()
      await button(panel, decision === 'reject' ? 'Rechazar propuesta' : 'Continuar con la reserva').trigger('click')
      await button(panel, decision === 'reject' ? 'Sí, rechazar' : 'Confirmar reserva').trigger('click')
      await flushPromises()
      expect(fetch).toHaveBeenLastCalledWith(path + '/8/decision', expect.objectContaining({
        method: 'POST', body: JSON.stringify({ decision, digest, confirm: true }),
      }))
      expect(panel.text()).toContain(decision === 'reject' ? 'No se modificó la reserva.' : 'No pude resolver la propuesta.')
      expect(panel.text()).not.toContain('Reserva confirmada.')
      panel.unmount()
    }
  })

  it('does not advertise the operation without its capability or accept inconsistent local time', async () => {
    const hidden = fakeFetch(result({}))
    const absent = await reservationPanel(hidden, false)
    expect(hidden.mock.calls.some(([url]) => url === path)).toBe(false)
    expect(absent.text()).not.toContain('reservation_confirm')
    absent.unmount()
    const fetch = fakeFetch(result({}), { ...confirmation, start_local: '2027-09-03T01:30:00+00:00' })
    const panel = await reservationPanel(fetch)
    await button(panel, 'Ver propuesta 8 de reserva').trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toContain('No pude abrir la propuesta.')
    expect(button(panel, 'Continuar con la reserva')).toBeUndefined()
    panel.unmount()
  })
})
