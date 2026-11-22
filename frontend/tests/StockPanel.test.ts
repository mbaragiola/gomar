import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App.vue'
import StockPanel from '../src/components/StockPanel.vue'

const reply = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body)))
const product = { producto_id: 1, nombre: 'Pan de prueba', activo: true, minimo: 1, saldo: -2, aviso: 'negativo' }
const low = { producto_id: 2, nombre: 'Café de prueba', activo: false, minimo: 2000, saldo: 1000, aviso: 'bajo' }

afterEach(() => vi.unstubAllGlobals())

describe('stock estimado del local', () => {
  it('muestra cantidades locales, alertas y ningún movimiento editable', async () => {
    const fetch = vi.fn(() => reply({ activo: true, productos: [product, low] }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(StockPanel, { props: { entity: 1, outlet: 2 } })
    await flushPromises()
    expect(panel.text()).toContain('El saldo es estimado')
    expect(panel.text()).toContain('Stock negativo')
    expect(panel.text()).toContain('Stock por debajo del mínimo')
    expect(panel.text()).toContain('Inactivo')
    expect(panel.findAll('button').map((button) => button.text())).toEqual(['Actualizar stock'])
    expect(fetch).toHaveBeenCalledWith('/api/v1/entities/1/outlets/2/pos/stock', expect.objectContaining({ cache: 'no-store' }))
    panel.unmount()
  })

  it('distingue un local desactivado de un local vacío y descarta respuestas demoradas', async () => {
    let resolve!: (value: Response) => void
    const deferred = new Promise<Response>((done) => { resolve = done })
    const fetch = vi.fn()
      .mockImplementationOnce(() => deferred)
      .mockImplementationOnce(() => reply({ activo: false, productos: [] }))
      .mockImplementationOnce(() => reply({ activo: true, productos: [] }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(StockPanel, { props: { entity: 1, outlet: 2 } })
    await panel.setProps({ outlet: 3 })
    await flushPromises()
    expect(fetch.mock.calls[0][1].signal.aborted).toBe(true)
    expect(panel.text()).toContain('desactivado para este local')
    resolve(await reply({ activo: true, productos: [product] }))
    await flushPromises()
    expect(panel.text()).not.toContain(product.nombre)
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('Todavía no hay productos')
    panel.unmount()
  })

  it.each([false, true])('no consulta stock si está cerrado (capacidad %s)', async (visible) => {
    const fetch = vi.fn((url: string) => {
      const path = url.split('?')[0]
      if (path === '/api/v1/auth/me') return reply({ usuario: 'personal-demo' })
      if (path === '/api/v1/entities') return reply([{ id: 1, nombre: 'Demo', capacidades: visible ? ['read_pos_stock'] : [] }])
      if (path.endsWith('/outlets')) return reply([{ id: 2, nombre: 'Centro' }])
      if (path.endsWith('/totals')) return reply({ cantidad: 0, totales: [] })
      if (path.endsWith('/pos/stock')) return reply({ activo: true, productos: [] })
      return reply([])
    })
    vi.stubGlobal('fetch', fetch)
    const app = mount(App)
    await flushPromises()
    const count = () => fetch.mock.calls.filter(([url]) => url.endsWith('/pos/stock')).length
    expect(count()).toBe(0)
    const button = app.findAll('button').find((candidate) => candidate.text() === 'Ver stock del local')
    expect(Boolean(button)).toBe(visible)
    if (button) {
      await button.trigger('click')
      await flushPromises()
      expect(count()).toBe(1)
    }
    app.unmount()
  })
})
