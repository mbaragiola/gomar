import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import McpConnect from '../src/components/McpConnect.vue'

const fake = 'X'.repeat(43)
const response = () => new Response(JSON.stringify({
  id: 31, token: fake, expires_at: new Date(Date.now() + 900_000).toISOString(),
}))
const empty = () => new Response(JSON.stringify({ access: [] }))
const attrs = { entity: 2, outlet: 7, outletName: 'Centro de prueba' }
const button = (panel: ReturnType<typeof mount>, name: string) => panel.findAll('button').find(b => b.text() === name)!

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('Conectar Claude o Codex', () => {
  it('emits one explicitly scoped link, conceals it, copies once and revokes it', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(response())
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)
    const clipboard = vi.fn(async () => {})
    Object.defineProperty(window.navigator, 'clipboard', { value: { writeText: clipboard }, configurable: true })
    const panel = mount(McpConnect, { props: attrs })
    expect(fetch).not.toHaveBeenCalled()
    await button(panel, 'Conectar Claude o Codex').trigger('click')
    expect(panel.text()).toContain('Centro de prueba')
    expect(fetch).toHaveBeenCalledExactlyOnceWith('/api/v1/entities/2/outlets/7/mcp-read-access', expect.objectContaining({
      credentials: 'same-origin', cache: 'no-store', referrerPolicy: 'no-referrer',
    }))
    await button(panel, 'Crear acceso').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenCalledWith('/api/v1/auth/mcp-token', expect.objectContaining({
      method: 'POST', body: '{"entity_id":2,"outlet_id":7}', credentials: 'same-origin', cache: 'no-store',
    }))
    expect(panel.text()).not.toContain(fake)
    expect(localStorage.getItem(fake)).toBeNull()
    await button(panel, 'Copiar clave').trigger('click')
    await flushPromises()
    expect(clipboard).toHaveBeenCalledExactlyOnceWith(fake)
    expect(button(panel, 'Copiar clave')).toBeUndefined()
    expect(button(panel, 'Mostrar una vez')).toBeUndefined()
    await button(panel, 'Cerrar conexión').trigger('click')
    await button(panel, 'Conectar Claude o Codex').trigger('click')
    expect(panel.text()).not.toContain(fake)
    await button(panel, 'Revocar acceso').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/auth/mcp-token/31', expect.objectContaining({ method: 'DELETE' }))
    expect(panel.text()).toContain('Acceso revocado.')
    panel.unmount()
  })

  it('clears a deliberately revealed key on browser navigation', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(empty()).mockResolvedValueOnce(response()))
    const panel = mount(McpConnect, { props: attrs })
    await button(panel, 'Conectar Claude o Codex').trigger('click')
    await button(panel, 'Crear acceso').trigger('click')
    await flushPromises()
    await button(panel, 'Mostrar una vez').trigger('click')
    expect(panel.text()).toContain(fake)
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()
    expect(panel.text()).not.toContain(fake)
    expect(button(panel, 'Mostrar una vez')).toBeUndefined()
    panel.unmount()
  })

  it('revokes an authorization arriving after the component disappears', async () => {
    let settle!: (value: Response) => void
    const pending = new Promise<Response>(resolve => { settle = resolve })
    const fetch = vi.fn().mockResolvedValueOnce(empty()).mockReturnValueOnce(pending).mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(McpConnect, { props: attrs })
    await button(panel, 'Conectar Claude o Codex').trigger('click')
    await button(panel, 'Crear acceso').trigger('click')
    panel.unmount()
    settle(response())
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/auth/mcp-token/31', expect.objectContaining({ method: 'DELETE' }))
  })

  it('recovers only same-outlet metadata and revokes exactly the row the operator picks', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'] })
    vi.setSystemTime(new Date('2026-11-01T22:00:00Z'))
    const metadata = { access: [
      { id: '9223372036854775807', issued_at: '2026-11-01T21:56:00Z', expires_at: '2026-11-01T22:01:00Z' },
      { id: '31', issued_at: '2026-11-01T21:55:00Z', expires_at: '2026-11-01T22:05:00Z' },
    ] }
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(metadata)))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(McpConnect, { props: attrs })
    await button(panel, 'Conectar Claude o Codex').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('Acceso 9223372036854775807')
    expect(panel.text()).toContain('Acceso 31')
    expect(button(panel, 'Mostrar una vez')).toBeUndefined()
    expect(button(panel, 'Copiar clave')).toBeUndefined()
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(panel.text()).not.toContain('Acceso 9223372036854775807')
    expect(fetch).toHaveBeenCalledTimes(1)
    await panel.get('[aria-label="Revocar acceso 31"]').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/auth/mcp-token/31', expect.objectContaining({ method: 'DELETE' }))
    expect(panel.text()).toContain('Acceso revocado.')
    expect(panel.text()).not.toContain('Acceso 31')
    panel.unmount()
  })

  it('never echoes response details in setup errors', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ detail: fake }), { status: 403 })))
    const panel = mount(McpConnect, { props: attrs })
    await button(panel, 'Conectar Claude o Codex').trigger('click')
    await button(panel, 'Crear acceso').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('No pude crear el acceso.')
    expect(panel.text()).not.toContain(fake)
    panel.unmount()
  })
})
