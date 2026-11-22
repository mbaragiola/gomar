import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import McpDocumentProposals from '../src/components/McpDocumentProposals.vue'

const base = '/api/v1/entities/2/outlets/7/document-draft-proposals'
const expiry = () => new Date(Date.now() + 900_000).toISOString()
const digest = 'a'.repeat(64)
const fakeKey = 'X'.repeat(43)
const draft = {
  supplier_id: 11, supplier_name: '<b>Proveedor de prueba</b>', type: 'invoice', letter: 'A',
  point_of_sale: 2, number: 42, issue_date: '2026-11-22', net_minor: 0, currency: 'ARS', total_minor: 501234,
}
const pending = () => ({ id: 8, status: 'pending', expires_at: expiry() })
const result = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
const get = (panel: ReturnType<typeof mount>, name: string) => panel.findAll('button').find(b => b.text() === name)!
const start = (fetch: unknown) => {
  vi.stubGlobal('fetch', fetch)
  return mount(McpDocumentProposals, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba' } })
}
async function open(panel: ReturnType<typeof mount>) {
  await get(panel, 'Propuestas de documentos').trigger('click')
  await flushPromises()
}

afterEach(() => vi.unstubAllGlobals())

describe('Propuestas de documentos', () => {
  it('renders reviewed draft inert and needs a separate explicit digest-bound creation', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), draft, digest }))
      .mockResolvedValueOnce(result({ id: 8, status: 'approved', document_id: 4, expires_at: expiry() }))
    const panel = start(fetch)
    expect(fetch).not.toHaveBeenCalled()
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    expect(panel.get('dl').text()).toContain(draft.supplier_name)
    expect(panel.find('dl b').exists()).toBe(false)
    expect(panel.get('dl').text()).toContain('ARS 5.012,34')
    expect(panel.get('dl').text()).toContain('0,00')
    expect(panel.get('dl').text()).toContain('No informado')
    expect(panel.get('dl').text()).toContain('Centro de prueba')
    expect(panel.get('dl').text()).toContain('Punto de venta 2 · Número 42')
    await get(panel, 'Continuar con este borrador').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    await get(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    await get(panel, 'Continuar con este borrador').trigger('click')
    await get(panel, 'Crear borrador').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(base + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: 'approve', digest, confirm: true }),
    }))
    expect(panel.text()).toContain('Borrador creado')
    panel.unmount()
  })

  it('clears stale preview and never displays the server error body', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), draft, digest }))
      .mockResolvedValueOnce(result({ detail: fakeKey }, 409))
      .mockResolvedValueOnce(result({ proposals: [] }))
    const panel = start(fetch)
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    await get(panel, 'Rechazar propuesta').trigger('click')
    await get(panel, 'Sí, rechazar').trigger('click')
    await flushPromises()
    expect(fetch.mock.calls[2][1]).toMatchObject({ body: JSON.stringify({ decision: 'reject', digest, confirm: true }) })
    expect(panel.text()).toContain('La propuesta cambió')
    expect(panel.text()).not.toContain(fakeKey)
    expect(panel.find('dl').exists()).toBe(false)
    panel.unmount()
  })

  it('rejects with a distinct confirmed action instead of creating a draft', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), draft, digest }))
      .mockResolvedValueOnce(result({ id: 8, status: 'rejected', expires_at: expiry() }))
    const panel = start(fetch)
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    await get(panel, 'Rechazar propuesta').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(get(panel, 'Crear borrador')).toBeUndefined()
    await get(panel, 'Sí, rechazar').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(base + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: 'reject', digest, confirm: true }),
    }))
    expect(panel.text()).toContain('No se creó ningún borrador')
    panel.unmount()
  })

  it('does not allow an incomplete preview even with a valid digest', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), draft: { ...draft, total_minor: null }, digest }))
    const panel = start(fetch)
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toBe('No pude abrir la propuesta.')
    expect(get(panel, 'Continuar con este borrador')).toBeUndefined()
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
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/auth/mcp-document-proposal-token', expect.objectContaining({
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
    expect(fetch.mock.calls[3][0]).toBe('/api/v1/auth/mcp-document-proposal-token/3')
    expect(panel.text()).toContain('Clave revocada')
    panel.unmount()
  })
})
