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
