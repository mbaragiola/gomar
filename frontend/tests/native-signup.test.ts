import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import NativeSignup from '../src/signup/NativeSignup.vue'

const challenge = 'a'.repeat(43)
const display = { organization: 'Organización de prueba', branches: ['Centro', 'Norte'] }
const response = () => Response.json({ ...display, challenge })
const status = (state: 'review' | 'confirmed' | 'prepared' = 'review') =>
  Response.json(state === 'review' ? { state } : { state, ...display })
const panel = (validAddress = true) => mount(NativeSignup, { props: { validAddress }, global: { stubs: ['ThemeSelect'] } })

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

describe('revisión nativa del alta', () => {
  it('primero consulta sin escribir y envía una sola confirmación explícita', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(status()).mockResolvedValueOnce(response()).mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await flushPromises()
    expect(fetch).toHaveBeenCalledWith('/api/v1/alta/estado', expect.objectContaining({ method: 'POST', body: '{}', cache: 'no-store' }))
    expect(fetch).toHaveBeenCalledTimes(1)
    await view.get('button').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenCalledWith('/api/v1/alta/revisar', expect.objectContaining({ method: 'POST', body: '{}' }))
    expect(view.get('h2').text()).toBe('Organización de prueba')
    expect(view.findAll('li').map((item) => item.text())).toEqual(['Centro', 'Norte'])
    const submit = view.get('button')
    await Promise.all([submit.trigger('click'), submit.trigger('click')])
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/alta/confirmar', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ challenge, confirm: true }),
    }))
    expect(view.get('h2').text()).toBe('Confirmación recibida')
    expect(view.text()).toContain('La puesta en marcha sigue pendiente')
    expect(view.text()).toContain('no acredita un pago ni activa el acceso')
    expect(view.find('button').exists()).toBe(false)
    view.unmount()
  })

  it.each([
    ['confirmed', 'Confirmación recibida'],
    ['prepared', 'Preparación registrada'],
  ] as const)('puede volver a abrir el estado %s sin reemitir la solicitud', async (state, title) => {
    const fetch = vi.fn().mockResolvedValueOnce(status(state))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await flushPromises()
    expect(view.get('h2').text()).toBe(title)
    expect(view.text()).toContain('Organización de prueba')
    expect(view.findAll('li').map((item) => item.text())).toEqual(['Centro', 'Norte'])
    expect(view.text()).toContain('no acredita un pago ni activa el acceso')
    expect(view.find('button').exists()).toBe(false)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch.mock.calls[0][0]).toBe('/api/v1/alta/estado')
    view.unmount()
  })

  it('borra un estado mostrado al salir y verifica antes de mostrarlo de nuevo', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(status('prepared')).mockResolvedValueOnce(new Response(null, { status: 404 }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await flushPromises()
    expect(view.text()).toContain('Organización de prueba')
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()
    expect(view.text()).not.toContain('Organización de prueba')
    window.dispatchEvent(new Event('pageshow'))
    await flushPromises()
    expect(view.get('h2').text()).toBe('Solicitud no disponible')
    expect(view.text()).not.toContain('Preparación registrada')
    expect(view.get('button').text()).toBe('Volver a consultar')
    expect(fetch.mock.calls.map(([path]) => path)).toEqual(['/api/v1/alta/estado', '/api/v1/alta/estado'])
    view.unmount()
  })

  it('espera a que se vea la pestaña que abrió oculta con una sesión anterior', async () => {
    let visible = false
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visible ? 'visible' : 'hidden')
    const fetch = vi.fn().mockResolvedValueOnce(new Response(null, { status: 404 }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await flushPromises()
    expect(fetch).not.toHaveBeenCalled()
    expect(view.text()).not.toContain('Organización de prueba')
    visible = true
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(fetch.mock.calls.map(([path]) => path)).toEqual(['/api/v1/alta/estado'])
    expect(view.get('h2').text()).toBe('Solicitud no disponible')
    expect(view.text()).not.toContain('Organización de prueba')
    view.unmount()
  })

  it('descarta el estado si quedó oculta antes de recibir la respuesta', async () => {
    let visible = true
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visible ? 'visible' : 'hidden')
    let release: (response: Response) => void = () => undefined
    const fetch = vi.fn().mockImplementationOnce(() => new Promise<Response>((resolve) => { release = resolve }))
      .mockResolvedValueOnce(new Response(null, { status: 404 }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    visible = false // The completion guard does not depend on delivery of the hidden event.
    release(status('prepared'))
    await flushPromises()
    expect(view.text()).not.toContain('Organización de prueba')
    expect(fetch).toHaveBeenCalledTimes(1)
    visible = true
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(fetch.mock.calls.map(([path]) => path)).toEqual(['/api/v1/alta/estado', '/api/v1/alta/estado'])
    expect(view.get('h2').text()).toBe('Solicitud no disponible')
    expect(view.text()).not.toContain('Organización de prueba')
    view.unmount()
  })

  it('permite salir sin escribir nada y oculta un alcance de una pestaña anterior', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(status()).mockImplementation(async () => response())
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await flushPromises()
    await view.get('button').trigger('click')
    await flushPromises()
    await view.findAll('button')[1].trigger('click')
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(view.text()).not.toContain('Organización de prueba')
    await view.get('button').trigger('click')
    await flushPromises()
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()
    expect(view.text()).not.toContain('Organización de prueba')
    expect(fetch).toHaveBeenCalledTimes(3)
    view.unmount()
  })

  it('no presenta controles de confirmación si la revisión no es válida', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(status()).mockResolvedValueOnce(Response.json({ challenge, organization: 'Sin alcance', branches: [] }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await flushPromises()
    await view.get('button').trigger('click')
    await flushPromises()
    expect(view.get('[role=alert]').text()).toContain('La revisión no está disponible')
    expect(view.text()).not.toContain('Confirmar solicitud')
    expect(fetch).toHaveBeenCalledTimes(2)
    view.unmount()
  })

  it('no permite reenviar automáticamente cuando no se conoce el resultado', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(status()).mockResolvedValueOnce(response()).mockRejectedValueOnce(new TypeError('connection lost'))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await flushPromises()
    await view.get('button').trigger('click')
    await flushPromises()
    await view.get('button').trigger('click')
    await flushPromises()
    expect(view.get('[role=alert]').text()).toContain('pudo haberse guardado')
    expect(view.find('button').exists()).toBe(false)
    expect(fetch).toHaveBeenCalledTimes(3)
    view.unmount()
  })

  it('descarta una respuesta de revisión que llega después de abandonar la vista', async () => {
    let release: (response: Response) => void = () => undefined
    const fetch = vi.fn().mockResolvedValueOnce(status()).mockImplementation(() => new Promise<Response>((resolve) => { release = resolve }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await flushPromises()
    await view.get('button').trigger('click')
    window.dispatchEvent(new Event('pagehide'))
    release(response())
    await flushPromises()
    expect(view.text()).not.toContain('Organización de prueba')
    expect(view.get('button').text()).toBe('Ver solicitud')
    view.unmount()
  })

  it('reserva enlaces modificados sin tocar ninguna ruta', async () => {
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    const view = panel(false)
    expect(view.get('h2').text()).toBe('Solicitud no disponible')
    expect(view.find('button').exists()).toBe(false)
    expect(fetch).not.toHaveBeenCalled()
    view.unmount()
  })
})
