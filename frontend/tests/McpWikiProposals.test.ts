import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import McpWikiProposals from '../src/components/McpWikiProposals.vue'

const base = '/api/v1/entities/2/outlets/7/wiki-create-proposals'
const expiry = () => new Date(Date.now() + 900_000).toISOString()
const digest = 'a'.repeat(64)
const fakeKey = 'X'.repeat(43)
const body = '<b>Texto literal</b>\nSegunda línea'
const pending = () => ({ id: 8, title: 'Trabajo del día', status: 'pending', expires_at: expiry() })
const result = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
const get = (panel: ReturnType<typeof mount>, name: string) => panel.findAll('button').find(b => b.text() === name)!
const start = (fetch: unknown) => {
  vi.stubGlobal('fetch', fetch)
  return mount(McpWikiProposals, { props: { entity: 2, outlet: 7, outletName: 'Centro de prueba' } })
}
async function open(panel: ReturnType<typeof mount>) {
  await get(panel, 'Propuestas del manual').trigger('click')
  await flushPromises()
}

afterEach(() => vi.unstubAllGlobals())

describe('Propuestas del manual', () => {
  it('renders reviewed text inert and needs a separate explicit digest-bound publish', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), body, digest }))
      .mockResolvedValueOnce(result({ id: 8, status: 'approved', article_id: 4, expires_at: expiry() }))
    const panel = start(fetch)
    expect(fetch).not.toHaveBeenCalled()
    await open(panel)
    await get(panel, 'Ver propuesta 8').trigger('click')
    await flushPromises()
    expect(panel.get('pre').element.textContent).toBe(body)
    expect(panel.find('pre b').exists()).toBe(false)
    await get(panel, 'Revisé el texto: continuar').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    await get(panel, 'Volver sin cambios').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    await get(panel, 'Revisé el texto: continuar').trigger('click')
    await get(panel, 'Aprobar y publicar').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith(base + '/8/decision', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ decision: 'approve', digest, confirm: true }),
    }))
    expect(panel.text()).toContain('Página publicada')
    panel.unmount()
  })

  it('clears stale preview and never displays the server error body', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(result({ proposals: [pending()] }))
      .mockResolvedValueOnce(result({ ...pending(), body, digest }))
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
    expect(panel.find('pre').exists()).toBe(false)
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
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/auth/mcp-proposal-token', expect.objectContaining({
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
    expect(fetch.mock.calls[3][0]).toBe('/api/v1/auth/mcp-proposal-token/3')
    expect(panel.text()).toContain('Clave revocada')
    panel.unmount()
  })
})
