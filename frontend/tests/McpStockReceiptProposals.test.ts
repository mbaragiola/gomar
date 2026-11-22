import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import McpStockReceiptProposals from '../src/components/McpStockReceiptProposals.vue'

const base = '/api/v1/entities/2/outlets/7/stock-receipt-proposals'
const expiry = () => new Date(Date.now() + 900_000).toISOString()
const digest = 'a'.repeat(64)
const fakeKey = 'X'.repeat(43)
const receipt = { product_id: 6, product_name: '<b>Pan de prueba</b>', product_revision: 3, quantity: 2500, stock_minimum: 0 }
const pending = () => ({ id: 8, status: 'pending', expires_at: expiry() })
const result = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
const get = (panel: ReturnType<typeof mount>, name: string) => panel.findAll('button').find(b => b.text() === name)!
const start = (fetch: unknown) => {
  vi.stubGlobal('fetch', fetch)
  return mount(McpStockReceiptProposals, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba' } })
}
async function open(panel: ReturnType<typeof mount>) {
  await get(panel, 'Recepciones de stock propuestas').trigger('click')
  await flushPromises()
}

afterEach(() => vi.unstubAllGlobals())

describe('Recepciones de stock propuestas', () => {
  it('shows inert product with exact +units and requires explicit digest-bound approval', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), receipt, digest }))
      .mockResolvedValueOnce(result({ id: 8, status: 'approved', movement_id: 4, expires_at: expiry() }))
    const panel = start(fetch)
    expect(fetch).not.toHaveBeenCalled()
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('Centro de prueba')
    expect(panel.text()).toContain('Producto: <b>Pan de prueba</b> (6)')
    expect(panel.text()).toContain('+2.500 unidades')
    expect(panel.text()).toContain('Mínimo configurado: 0')
    expect(panel.find('section section b').exists()).toBe(false)
    await get(panel, 'Continuar con esta recepción').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    await get(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    await get(panel, 'Continuar con esta recepción').trigger('click')
    await get(panel, 'Registrar recepción').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(base + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: 'approve', digest, confirm: true }),
    }))
    expect(panel.text()).toContain('Recepción de stock registrada')
    panel.unmount()
  })

  it('clears stale preview and never displays the server error body', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), receipt, digest }))
      .mockResolvedValueOnce(result({ detail: fakeKey }, 409))
      .mockResolvedValueOnce(result({ proposals: [] }))
    const panel = start(fetch)
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    await get(panel, 'Continuar con esta recepción').trigger('click')
    await get(panel, 'Registrar recepción').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[2][1]).toMatchObject({ body: JSON.stringify({ decision: 'approve', digest, confirm: true }) })
    expect(panel.text()).toContain('No se registró la recepción')
    expect(panel.text()).not.toContain(fakeKey)
    expect(panel.find('section[aria-label="Vista previa de la propuesta"]').exists()).toBe(false)
    expect(fetch.mock.calls.filter(call => call[1].method === 'POST')).toHaveLength(1)
    panel.unmount()
  })

  it('rejects without changing stock', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), receipt, digest }))
      .mockResolvedValueOnce(result({ ...pending(), status: 'rejected' }))
    const panel = start(fetch)
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    await get(panel, 'Rechazar propuesta').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    await get(panel, 'Sí, rechazar').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(base + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: 'reject', digest, confirm: true }),
    }))
    expect(panel.text()).toContain('El stock no cambió')
    panel.unmount()
  })

  it.each([0, -1, 1_000_001, 1.5])('refuses a malformed quantity %s even with a valid digest', async quantity => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), receipt: { ...receipt, quantity }, digest }))
    const panel = start(fetch)
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toBe('No pude abrir la propuesta.')
    expect(get(panel, 'Continuar con esta recepción')).toBeUndefined()
    panel.unmount()
  })

  it('uses a distinct scoped key, hides it when closed and revokes using 204', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(result({ id: 3, token: fakeKey, expires_at: expiry() }, 201))
      .mockResolvedValueOnce(result({ proposals: [] }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(result({ proposals: [] }))
    const panel = start(fetch)
    await open(panel)
    await get(panel, 'Crear clave para propuestas').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/auth/mcp-stock-receipt-proposal-token', expect.objectContaining({
      method: 'POST', body: '{"entity_id":2,"outlet_id":7}',
    }))
    expect(panel.text()).not.toContain(fakeKey)
    await get(panel, 'Mostrar clave una vez').trigger('click')
    expect(panel.text()).toContain(fakeKey)
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()
    expect(panel.text()).not.toContain(fakeKey)
    await open(panel)
    expect(panel.text()).not.toContain(fakeKey)
    await get(panel, 'Revocar clave para propuestas').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[3][0]).toBe('/api/v1/auth/mcp-stock-receipt-proposal-token/3')
    expect(panel.text()).toContain('Clave revocada')
    panel.unmount()
  })
})
