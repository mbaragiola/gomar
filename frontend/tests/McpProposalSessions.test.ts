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
