import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import SaleTimeEvidence from '../src/pos/SaleTimeEvidence.vue'
import { draft, saleRequest, type Grant } from '../src/pos/model'
import { lookupClock, receiptClock, saleTimeHandoff, saleTimeTarget } from '../src/pos/sale-time'
import { request } from '../src/api'

vi.mock('../src/api', () => ({ request: vi.fn() }))
const http = vi.mocked(request)
const id = '92000000-0000-4000-8000-000000000001'
const target = { id, entity: 1, outlet: 1 }
const till = '2026-12-01T02:58:00.000Z'
const registered = '2026-12-01T03:07:00Z'
const handoff = saleTimeHandoff(target)
const server = { request_id: id, till_reported_at_utc: till, registered_at_utc: registered,
  till_time_status: 'unverified', handoff_url: handoff }

const created = Date.parse(till)
const grant: Grant = { actor: 2, entity: 1, outlet: 1, lease: 'synthetic', label: 'Centro', expires: created + 1000, catalog: [
  { id: 1, nombre: 'Pan de prueba', precio_centavos: 300000, revision: 1, activo: true },
] }

afterEach(() => { vi.resetAllMocks(); vi.restoreAllMocks() })

describe('evidencia de horario de Caja', () => {
  it('conserva la captura original ante reintentos y solo la envía a un servidor que anuncia soporte', () => {
    const basket = new Map([[1, 1]])
    const old = draft(grant, basket, id, created, 'efectivo')
    const updated = draft({ ...grant, tillTimeV1: true }, basket, id, created, 'efectivo')
    expect(old.tillReportedAt).toBeUndefined()
    expect(updated.tillReportedAt).toBe(till)
    expect(saleRequest(old, true)).not.toHaveProperty('hora_informada_caja_utc')
    expect(saleRequest(updated, false)).toEqual(saleRequest(old, true))
    expect(saleRequest(updated, true)).toEqual({ ...saleRequest(old), hora_informada_caja_utc: till })
    expect(saleRequest({ ...updated, created: created + 9000 }, true)).toEqual(saleRequest(updated, true))
    expect(updated.tillReportedAt).toBe(till)
  })

  it('mantiene la ausencia, los relojes separados y el estado que indica el servidor', () => {
    expect(receiptClock({ creado_en: registered })).toEqual({ registeredAt: registered, status: 'not_reported' })
    expect(receiptClock({ creado_en: registered, hora_informada_caja_utc: till, estado_hora_caja: 'review' })).toEqual({
      registeredAt: registered, tillReportedAt: till, status: 'review',
    })
    expect(receiptClock({ creado_en: registered, hora_informada_caja_utc: '2026-11-31T02:58:00Z', estado_hora_caja: 'review' })).toBeUndefined()
    expect(lookupClock({ ...server, handoff_url: 'https://not-gomar.invalid' }, target)).toBeUndefined()
    expect(lookupClock(server, { ...target, outlet: 2 })).toBeUndefined()
  })

  it('solo consulta el enlace exacto al pulsar y muestra ambas horas locales sin afirmar pago', async () => {
    expect(saleTimeTarget(handoff.slice(4))).toEqual(target)
    expect(saleTimeTarget(handoff.slice(4).replace('outlet=1', 'outlet=01'))).toBeUndefined()
    http.mockResolvedValue(server)
    const panel = mount(SaleTimeEvidence, { props: { target } })
    expect(http).not.toHaveBeenCalled()
    expect(panel.text()).toContain('no acredita cuándo ocurrió la venta, un pago ni un comprobante fiscal')
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(http).toHaveBeenCalledWith(`/entities/1/outlets/1/pos/sale-times/${id}`, expect.objectContaining({ cache: 'no-store' }))
    expect(panel.text()).toContain('Registrada por Gomar:')
    expect(panel.text()).toContain('Hora informada por la caja (no verificada):')
    expect(panel.findAll('time').map(el => el.attributes('datetime'))).toEqual([registered, till])
    expect(panel.findAll('time').map(el => el.text())).toEqual([expect.stringContaining('00:07'), expect.stringContaining('23:58')])
    panel.unmount()
  })

  it('no muestra datos al perder acceso ni acepta una respuesta que llega después de cambiar el enlace', async () => {
    const online = vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false)
    const panel = mount(SaleTimeEvidence, { props: { target } })
    expect(panel.get('button').attributes('disabled')).toBeDefined()
    expect(http).not.toHaveBeenCalled()
    online.mockReturnValue(true)
    window.dispatchEvent(new Event('online'))
    await flushPromises()
    let resolve!: (data: unknown) => void
    http.mockReturnValueOnce(new Promise(r => { resolve = r }))
    await panel.get('button').trigger('click')
    await panel.setProps({ target: { ...target, outlet: 2 } })
    resolve(server)
    await flushPromises()
    expect(panel.findAll('time')).toHaveLength(0)
    http.mockRejectedValueOnce(new Error(till))
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(panel.get('[role="alert"]').text()).toBe('No se pudo consultar la referencia con esta cuenta y local.')
    expect(panel.text()).not.toContain(till)
    panel.unmount()
  })
})
