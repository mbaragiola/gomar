import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App.vue'
import ProviderSignIn from '../src/components/ProviderSignIn.vue'

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const config = (providers: unknown[], local_password_enabled: unknown = true, admin_recovery_enabled: unknown = false, employee_password_enabled: unknown = false) => json({ providers, local_password_enabled, admin_recovery_enabled, employee_password_enabled })
const known = [{ id: 'google', label: 'Google' }, { id: 'microsoft' }, { id: 'microsoft-work' }]
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
    complete(config([...known, { id: 'google' }, { id: 'apple' }, { id: 'custom', label: '<b>No</b>' }]))
    await flushPromises()
    expect(panel.findAll('button').map(b => b.text())).toEqual([
      'Continuar con Google', 'Continuar con Microsoft',
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
      if (url === base) return Promise.resolve(config([], true))
      return Promise.resolve(url.endsWith('/auth/me') ? json({}, 401) : json({}))
    })
    vi.stubGlobal('fetch', fetch)
    const panel = mount(App)
    await flushPromises()
    expect(panel.get('form').text()).toContain('Contraseña')
    expect(panel.findAll('button').some(b => b.text().startsWith('Continuar con'))).toBe(false)
    panel.unmount()
  })

  it('hides ordinary password sign-in for the provider-only policy and shows separate recovery', async () => {
    const fetch = vi.fn((url: string) => {
      if (url === base) return Promise.resolve(config(known, false, true))
      return Promise.resolve(url.endsWith('/auth/me') ? json({}, 401) : json({}))
    })
    vi.stubGlobal('fetch', fetch)
    const panel = mount(App)
    await flushPromises()
    expect(panel.find('form').exists()).toBe(false)
    expect(panel.get('a[href="/admin/login/"]').text()).toBe('Recuperación para administradores')
    expect(panel.findAll('button').filter(b => b.text().startsWith('Continuar con'))).toHaveLength(3)
    expect(fetch.mock.calls.some(([url]) => url.endsWith('/auth/login'))).toBe(false)
    panel.unmount()
  })

  it.each([
    { name: 'network error', options: () => json({}, 503) },
    { name: 'non-boolean flags', options: () => config([], 'false', 'true') },
  ])('hides password and recovery options on $name', async ({ options }) => {
    const fetch = vi.fn((url: string) => {
      if (url === base) return Promise.resolve(options())
      return Promise.resolve(url.endsWith('/auth/me') ? json({}, 401) : json({}))
    })
    vi.stubGlobal('fetch', fetch)
    const panel = mount(App)
    await flushPromises()
    expect(panel.find('form').exists()).toBe(false)
    expect(panel.find('a[href="/admin/login/"]').exists()).toBe(false)
    expect(panel.findAll('button').some(b => b.text() === 'Ingresar como empleado')).toBe(false)
    panel.unmount()
  })

  it('exposes the employee form but not classic passwords in strict hybrid mode', async () => {
    const fetch = vi.fn((url: string) => {
      if (url === base) return Promise.resolve(config(known, false, true, true))
      return Promise.resolve(url.endsWith('/auth/me') ? json({}, 401) : json({}))
    })
    vi.stubGlobal('fetch', fetch)
    const panel = mount(App)
    await flushPromises()
    expect(panel.find('form').exists()).toBe(false)
    expect(panel.findAll('button').filter(b => b.text().startsWith('Continuar con'))).toHaveLength(3)
    const employeeEntry = panel.findAll('button').find(b => b.text() === 'Ingresar como empleado')!
    await employeeEntry.trigger('click')
    expect(panel.get('form[aria-label="Ingreso de empleados"]').text()).toContain('Número de organización')
    expect(panel.findAll('form')).toHaveLength(1)
    expect(fetch.mock.calls.some(([url]) => url.endsWith('/auth/login'))).toBe(false)
    panel.unmount()
  })

  it('does not offer passwords when explicitly disabled but the provider list is incomplete', async () => {
    const fetch = vi.fn((url: string) => {
      if (url === base) return Promise.resolve(config([known[0]], false, false))
      return Promise.resolve(url.endsWith('/auth/me') ? json({}, 401) : json({}))
    })
    vi.stubGlobal('fetch', fetch)
    const panel = mount(App)
    await flushPromises()
    expect(panel.find('form').exists()).toBe(false)
    expect(panel.get('[aria-label="Ingreso con un proveedor"]').text()).toContain('no están disponibles')
    expect(panel.find('a[href="/admin/login/"]').exists()).toBe(false)
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
