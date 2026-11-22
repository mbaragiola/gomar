import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App.vue'
import DeliveryInbox from '../src/components/DeliveryInbox.vue'

const stamp = '2030-04-08T22:30:00Z'
const example = (ref: string, estado = 'aceptado', items = [{ nombre: 'Milanesa de prueba', cantidad: 2 }]) => ({
  id: ref, proveedor: 'pedidosya', pedido_externo: ref, estado, items, recibido_en: stamp, actualizado_en: stamp,
})

function reply(payload: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(payload), { status }))
}

afterEach(() => vi.unstubAllGlobals())

describe('bandeja de delivery', () => {
  it('muestra pedidos aceptados y cancelados sin acciones ni datos adicionales', async () => {
    const fetch = vi.fn(() => reply({ pedidos: [example('DEMO-A'), example('DEMO-B', 'cancelado', [])] }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(DeliveryInbox, { props: { entity: 1, outlet: 2 } })
    expect(panel.text()).toContain('Cargando pedidos')
    await flushPromises()
    expect(panel.text()).toContain('PedidosYa · DEMO-A')
    expect(panel.text()).toContain('Aceptado')
    expect(panel.text()).toContain('2 × Milanesa de prueba')
    expect(panel.text()).toContain('Cancelado')
    expect(panel.text()).toContain('No se informaron productos')
    expect(panel.findAll('button').map((button) => button.text())).toEqual(['Actualizar pedidos'])
    expect(fetch).toHaveBeenCalledWith('/api/v1/entities/1/outlets/2/delivery/orders', expect.objectContaining({ cache: 'no-store' }))
    panel.unmount()
  })

  it('distingue un pedido recibido de Rappi de uno aceptado por la plataforma', async () => {
    vi.stubGlobal('fetch', vi.fn(() => reply({ pedidos: [
      { ...example('RAPPI-DEMO', 'recibido'), proveedor: 'rappi' },
      { ...example('RAPPI-CANCELADO', 'cancelado', []), proveedor: 'rappi' },
    ] })))
    const panel = mount(DeliveryInbox, { props: { entity: 1, outlet: 2 } })
    await flushPromises()
    expect(panel.text()).toContain('Rappi · RAPPI-DEMO')
    expect(panel.findAll('strong').map((label) => label.text())).toEqual(['Recibido', 'Cancelado'])
    expect(panel.text()).not.toContain('Aceptado')
    expect(panel.findAll('button').map((button) => button.text())).toEqual(['Actualizar pedidos'])
    panel.unmount()
  })

  it('maneja 404 y permite reintentar sin afectar el resto de la pantalla', async () => {
    const fetch = vi.fn().mockImplementationOnce(() => reply({}, 404)).mockImplementationOnce(() => reply({ pedidos: [] }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(DeliveryInbox, { props: { entity: 1, outlet: 2 } })
    await flushPromises()
    expect(panel.get('[role=alert]').text()).toContain('no está habilitada para este local')
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(panel.text()).toContain('Todavía no hay pedidos en este local.')
    panel.unmount()
  })

  it('aborta y descarta una respuesta demorada al cambiar de local', async () => {
    let finish!: (value: Response) => void
    const deferred = new Promise<Response>((resolve) => { finish = resolve })
    const fetch = vi.fn()
      .mockImplementationOnce(() => deferred)
      .mockImplementationOnce(() => reply({ pedidos: [example('DEMO-NORTE')] }))
    vi.stubGlobal('fetch', fetch)
    const panel = mount(DeliveryInbox, { props: { entity: 1, outlet: 2 } })
    await panel.setProps({ outlet: 3 })
    await flushPromises()
    expect(fetch.mock.calls[0][1].signal.aborted).toBe(true)
    finish(await reply({ pedidos: [example('DEMO-CENTRO')] }))
    await flushPromises()
    expect(panel.text()).toContain('DEMO-NORTE')
    expect(panel.text()).not.toContain('DEMO-CENTRO')
    panel.unmount()
  })

  it.each([false, true])('no consulta el API con la bandeja cerrada (capacidad %s)', async (enabled) => {
    const fetch = vi.fn((url: string) => {
      const path = url.split('?')[0]
      if (path === '/api/v1/auth/me') return reply({ usuario: 'personal-demo' })
      if (path === '/api/v1/entities') return reply([{ id: 1, nombre: 'Demo', capacidades: enabled ? ['read_business_data', 'read_delivery_orders'] : ['read_business_data'] }])
      if (path.endsWith('/outlets')) return reply([{ id: 2, nombre: 'Centro' }])
      if (path.endsWith('/totals')) return reply({ cantidad: 0, totales: [] })
      if (path.endsWith('/delivery/orders')) return reply({ pedidos: [] })
      return reply([])
    })
    vi.stubGlobal('fetch', fetch)
    const app = mount(App)
    await flushPromises()
    const opened = () => fetch.mock.calls.filter(([url]) => url.endsWith('/delivery/orders')).length
    expect(opened()).toBe(0)
    const button = app.findAll('button').find((candidate) => candidate.text() === 'Abrir bandeja de delivery')
    expect(Boolean(button)).toBe(enabled)
    if (button) {
      await button.trigger('click')
      await flushPromises()
      expect(opened()).toBe(1)
      await app.findAll('button').find((candidate) => candidate.text() === 'Cerrar bandeja de delivery')?.trigger('click')
      expect(app.find('[aria-label="Bandeja de delivery"]').exists()).toBe(false)
      expect(opened()).toBe(1)
    }
    app.unmount()
  })
})
