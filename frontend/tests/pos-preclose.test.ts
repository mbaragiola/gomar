import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PrecloseSummary from '../src/pos/PrecloseSummary.vue'
import { scopeKey, type Grant, type Sale } from '../src/pos/model'
import { precloseSummary } from '../src/pos/preclose'
import { localLease, readRegisterSales } from '../src/pos/storage'

vi.mock('../src/pos/storage', () => ({ localLease: vi.fn(), readRegisterSales: vi.fn() }))

const scope: Grant = {
  actor: 3, entity: 1, outlet: 2, lease: 'lease', expires: Date.parse('2026-01-01T10:00:00Z'),
  label: 'Centro', catalog: [], tillTimeV1: true,
}
const now = Date.parse('2026-01-01T04:00:00Z')
const id = '92000000-0000-4000-8000-000000000001'
const registeredAt = '2026-01-01T03:00:00.000001Z'
const tillReportedAt = '2026-01-01T02:59:59.999Z'
const row = (id: string, fields: Partial<Sale> = {}): Sale => ({
  actor: scope.actor, entity: scope.entity, outlet: scope.outlet, key: scopeKey(scope),
  id, lines: [], total: 600, created: Date.parse('2026-01-01T02:00:00Z'), status: 'synced', ...fields,
})
const between = row(id, { receiptClock: { registeredAt, tillReportedAt, status: 'unverified' } })

beforeEach(() => {
  vi.setSystemTime(now)
  vi.mocked(localLease).mockReturnValue(scope.lease)
})
afterEach(() => {
  vi.useRealTimers()
  vi.resetAllMocks()
})

describe('resumen local de Caja antes del cierre', () => {
  it('usa los meses de Buenos Aires y solo el par original confirmado por Gomar', () => {
    const summary = precloseSummary(scope, [
      between,
      row('mismo-mes', { receiptClock: { registeredAt: '2026-01-01T02:59:59Z', tillReportedAt: '2025-12-31T23:59:59Z', status: 'unverified' } }),
      row('mes-anterior', { receiptClock: { registeredAt: '2026-01-01T02:59:59Z', tillReportedAt: '2025-12-01T02:59:59Z', status: 'review' } }),
      row('viejo', { receiptClock: { registeredAt: '2025-12-01T02:59:59Z', tillReportedAt: '2025-11-01T02:59:59Z', status: 'review' } }),
      row('sin-hora-caja', { tillReportedAt, receiptClock: { registeredAt, status: 'not_reported' } }),
      row('malformada', { receiptClock: { registeredAt, tillReportedAt: '2025-11-31T00:00:00Z', status: 'review' } }),
      row('vieja-sin-hora-caja', { receiptClock: { registeredAt: '2025-11-01T12:00:00Z', status: 'not_reported' } }),
      row('sin-registro', { tillReportedAt }),
      row('reintento-no-reemplaza', { tillReportedAt, receiptClock: { registeredAt, tillReportedAt: registeredAt, status: 'unverified' } }),
    ], now)
    expect([summary.previous, summary.current]).toEqual(['2025-12', '2026-01'])
    expect(summary.crossMonth.map((sale) => sale.id)).toEqual([id, 'mes-anterior'])
    expect(summary.unknownRegistration).toBe(1)
    expect(summary.unknownTill).toBe(2)
    // Even when UTC has already changed months, the window follows BA.
    expect(precloseSummary(scope, [], Date.parse('2026-01-01T02:59:59Z')).current).toBe('2025-12')
  })

  it('no mezcla cuentas, entidades, locales, ni supone que una cola pendiente no llegó a Gomar', () => {
    const summary = precloseSummary(scope, [
      row('cola-antigua', { created: 0, status: 'pending' }),
      row('revisar', { status: 'attention', tillReportedAt }),
      row('otra-cuenta', { actor: 4, status: 'pending' }),
      row('otro-local', { outlet: 5, status: 'attention' }),
      row('otra-entidad', { entity: 5 }), { ...between, actor: 4 },
    ], now)
    expect(summary).toMatchObject({ pending: 1, attention: 1, unknownRegistration: 0, crossMonth: [] })
    expect(summary.outstanding).toEqual([
      { id: 'cola-antigua', status: 'pending', tillReportedAt: undefined },
      { id: 'revisar', status: 'attention', tillReportedAt },
    ])
  })

  it('consulta explícita de solo lectura, muestra alcance, dos horarios y desconocidos', async () => {
    vi.mocked(readRegisterSales).mockResolvedValue([between, row('pendiente-sin-hora', { status: 'pending' }), row('para-revisar', { status: 'attention', tillReportedAt }), row('sin-registro')])
    const panel = mount(PrecloseSummary, { props: { scope, updating: false } })
    expect(readRegisterSales).not.toHaveBeenCalled()
    expect(panel.text()).toContain('Otras cajas sin verificar.')
    expect(panel.text()).toContain('Antes de contar el stock o cerrar el mes')
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(readRegisterSales).toHaveBeenCalledExactlyOnceWith(scope, now)
    expect(panel.get('[aria-label="Ventas pendientes o para revisar"]').text()).toContain('Registro de Gomar: sin confirmar desde este navegador.')
    expect(panel.text()).toContain('Pendientes de sincronizar: 1. Necesitan revisión: 1.')
    expect(panel.text()).toContain('Hora informada por la caja: desconocida.')
    expect(panel.text()).toContain('Fecha de registro desconocida en este navegador: 1')
    const crossing = panel.get('[aria-label="Ventas sincronizadas con horarios en meses distintos"]')
    expect(crossing.findAll('time').map((time) => time.attributes('datetime'))).toEqual([registeredAt, tillReportedAt])
    expect(crossing.get('a').attributes('href')).toBe('/pos#entity=1&outlet=2&venta=' + id)
    expect(panel.text()).toContain('12/2025 y 01/2026')
    panel.unmount()
  })

  it('no declara un cierre listo si falta información, incluso con cola vacía', async () => {
    vi.mocked(readRegisterSales).mockResolvedValueOnce(undefined).mockResolvedValueOnce([])
    const panel = mount(PrecloseSummary, { props: { scope, updating: false } })
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(panel.get('[role="alert"]').text()).toContain('No se pudieron leer')
    expect(panel.text()).not.toContain('Pendientes de sincronizar: 0')
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(panel.find('[role="alert"]').exists()).toBe(false)
    expect(panel.text()).toContain('Pendientes de sincronizar: 0. Necesitan revisión: 0.')
    expect(panel.text()).toContain('Otras cajas sin verificar.')
    expect(panel.text()).not.toMatch(/todas.*verificadas|cierre listo/i)
    panel.unmount()
  })

  it.each(['lease', 'expiry', 'sync', 'unmount'])('no publica una consulta anterior al cambio de %s', async (change) => {
    let finish!: (sales: Sale[]) => void
    vi.mocked(readRegisterSales).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    const panel = mount(PrecloseSummary, { props: { scope, updating: false } })
    await panel.get('button').trigger('click')
    if (change === 'lease') vi.mocked(localLease).mockReturnValue('another-lease')
    if (change === 'expiry') vi.setSystemTime(scope.expires)
    if (change === 'sync') await panel.setProps({ updating: true })
    if (change === 'unmount') panel.unmount()
    finish([between])
    await flushPromises()
    expect(panel.text()).not.toContain(id)
    if (change !== 'unmount') panel.unmount()
  })
})
