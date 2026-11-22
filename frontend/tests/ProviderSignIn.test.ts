import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App.vue'
import ProviderSignIn from '../src/components/ProviderSignIn.vue'

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const config = (providers: unknown[], local_password_enabled = true) => json({ providers, local_password_enabled })
const known = [{ id: 'google', label: 'Google' }, { id: 'apple' }, { id: 'microsoft' }, { id: 'microsoft-work' }]
const base = '/api/v1/auth/providers'

afterEach(() => vi.unstubAllGlobals())

describe('Ingreso con proveedores', () => {
  it('shows no options before a valid list and ignores unknown or duplicate choices', async () => {
    let complete!: (value: Response) => void
    const fetch = vi.fn().mockReturnValue(new Promise<Response>(resolve => { complete = resolve }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(ProviderSignIn, { props: { disabled: false } })
    expect(panel.get('[role=status]').text()).toContain('Consultando')
    expect(panel.findAll('button')).toHaveLength(0)
    complete(config([...known, { id: 'google' }, { id: 'custom', label: '<b>No</b>' }]))
    await flushPromises()
    expect(panel.findAll('button').map(b => b.text())).toEqual([
      'Continuar con Google', 'Continuar con Apple', 'Continuar con Microsoft',
      'Continuar con Microsoft (trabajo o estudio)',
    ])
    expect(fetch).toHaveBeenCalledWith(base, expect.objectContaining({ cache: 'no-store' }))
    await panel.setProps({ disabled: true })
    expect(panel.findAll('button').every(b => b.attributes('disabled') !== undefined)).toBe(true)
    expect(fetch).toHaveBeenCalledTimes(1)
    panel.unmount()
  })

  it('keeps existing password entry when the provider feature is off', async () => {
    const fetch = vi.fn((url: string) => {
      if (url === base) return Promise.resolve(config([], false))
      return Promise.resolve(url.endsWith('/auth/me') ? json({}, 401) : json({}))
    })
    vi.stubGlobal('fetch', fetch)
    const panel = mount(App)
    await flushPromises()
    expect(panel.get('form').text()).toContain('Contraseña')
    expect(panel.findAll('button').some(b => b.text().startsWith('Continuar con'))).toBe(false)
    panel.unmount()
  })

  it('starts only on deliberate click and serializes provider and password actions', async () => {
    let complete!: (value: Response) => void
    const fetch = vi.fn()
      .mockResolvedValueOnce(config(known))
      .mockReturnValueOnce(new Promise<Response>(resolve => { complete = resolve }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(ProviderSignIn, { props: { disabled: false } })
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(1)
    await panel.get('button').trigger('click')
    expect(panel.findAll('button').every(b => b.attributes('disabled') !== undefined)).toBe(true)
    expect(panel.emitted('busy')).toEqual([[true]])
    expect(fetch).toHaveBeenLastCalledWith(base + '/google/start', expect.objectContaining({ method: 'POST', cache: 'no-store' }))
    expect(fetch.mock.calls[1][1].headers.has('X-CSRFToken')).toBe(true)
    await panel.get('button').trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    complete(json({ detail: 'Raw private error' }, 503))
    await flushPromises()
    expect(panel.get('[role=alert]').text()).not.toContain('Raw private error')
    expect(panel.emitted('busy')).toEqual([[true], [false]])
    panel.unmount()
  })

  it('does not hand over to an unrelated destination', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(config(known)).mockResolvedValueOnce(json({
      authorization_url: 'https://example.invalid/o/oauth2/v2/auth',
    }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(ProviderSignIn, { props: { disabled: false } })
    await flushPromises()
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toContain('No pude iniciar')
    panel.unmount()
  })

  it('drops an in-flight reply after the login page is hidden', async () => {
    let complete!: (value: Response) => void
    const fetch = vi.fn().mockResolvedValueOnce(config(known))
      .mockReturnValueOnce(new Promise<Response>(resolve => { complete = resolve }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(ProviderSignIn, { props: { disabled: false } })
    await flushPromises()
    await panel.get('button').trigger('click')
    window.dispatchEvent(new Event('pagehide'))
    complete(json({ authorization_url: 'https://accounts.google.com/o/oauth2/v2/auth?state=synthetic' }))
    await flushPromises()
    expect(panel.emitted('busy')).toEqual([[true], [false]])
    expect(panel.find('[role=alert]').exists()).toBe(false)
    panel.unmount()
  })
})
