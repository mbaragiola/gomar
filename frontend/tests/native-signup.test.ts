import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import NativeSignup from '../src/signup/NativeSignup.vue'

const challenge = 'a'.repeat(43)
const response = () => Response.json({
  organization: 'Organización de prueba',
  branches: ['Centro', 'Norte'],
  challenge,
})
const panel = (validAddress = true) => mount(NativeSignup, { props: { validAddress }, global: { stubs: ['ThemeSelect'] } })

afterEach(() => { vi.unstubAllGlobals() })

describe('revisión nativa del alta', () => {
  it('muestra el alcance y envía una sola confirmación explícita, sin datos privados', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response()).mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    expect(fetch).not.toHaveBeenCalled()
    await view.get('button').trigger('click')
    await flushPromises()
    expect(fetch).toHaveBeenCalledWith('/api/v1/alta/revisar', expect.objectContaining({ method: 'POST', body: '{}' }))
    expect(view.get('h2').text()).toBe('Organización de prueba')
    expect(view.findAll('li').map((item) => item.text())).toEqual(['Centro', 'Norte'])
    const submit = view.get('button')
    await Promise.all([submit.trigger('click'), submit.trigger('click')])
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/alta/confirmar', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ challenge, confirm: true }),
    }))
    expect(view.get('h2').text()).toBe('Confirmación recibida')
    expect(view.text()).toContain('La puesta en marcha sigue pendiente')
    expect(view.text()).toContain('no acredita un pago ni activa el acceso')
    expect(view.find('button').exists()).toBe(false)
    view.unmount()
  })

  it('permite salir sin escribir nada y oculta un alcance de una pestaña anterior', async () => {
    const fetch = vi.fn().mockImplementation(async () => response())
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await view.get('button').trigger('click')
    await flushPromises()
    await view.findAll('button')[1].trigger('click')
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(view.text()).not.toContain('Organización de prueba')
    await view.get('button').trigger('click')
    await flushPromises()
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()
    expect(view.text()).not.toContain('Organización de prueba')
    expect(fetch).toHaveBeenCalledTimes(2)
    view.unmount()
  })

  it('no presenta controles si la respuesta no contiene una revisión válida', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(Response.json({ challenge, organization: 'Sin alcance', branches: [] }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await view.get('button').trigger('click')
    await flushPromises()
    expect(view.get('[role=alert]').text()).toContain('La revisión no está disponible')
    expect(view.text()).not.toContain('Confirmar solicitud')
    expect(fetch).toHaveBeenCalledTimes(1)
    view.unmount()
  })

  it('no permite reenviar automáticamente cuando no se conoce el resultado', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response()).mockRejectedValueOnce(new TypeError('connection lost'))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
    await view.get('button').trigger('click')
    await flushPromises()
    await view.get('button').trigger('click')
    await flushPromises()
    expect(view.get('[role=alert]').text()).toContain('pudo haberse guardado')
    expect(view.find('button').exists()).toBe(false)
    expect(fetch).toHaveBeenCalledTimes(2)
    view.unmount()
  })

  it('descarta una respuesta de revisión que llega después de abandonar la vista', async () => {
    let release: (response: Response) => void = () => undefined
    const fetch = vi.fn().mockImplementation(() => new Promise<Response>((resolve) => { release = resolve }))
    vi.stubGlobal('fetch', fetch)
    const view = panel()
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
