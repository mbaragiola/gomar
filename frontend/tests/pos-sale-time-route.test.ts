import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PosRoute from '../src/pos/PosRoute.vue'
import { draft, type Grant } from '../src/pos/model'
import { readGrant, recordOutcome, pendingSalesFor, salesPage } from '../src/pos/storage'

vi.mock('../src/pos/storage', () => ({
  localLease: vi.fn(() => 'active-lease'), readGrant: vi.fn(), salesPage: vi.fn(), pendingSalesFor: vi.fn(), readTableState: vi.fn(async () => ({})),
  recordOutcome: vi.fn(), acknowledgeTableRecovery: vi.fn(), arm: vi.fn(), beginTableSave: vi.fn(),
  broadcastPOSLock: vi.fn(), disarm: vi.fn(), enqueue: vi.fn(), finishTableSave: vi.fn(), forgetLease: vi.fn(),
  lockOfflinePOS: vi.fn(), rememberLease: vi.fn(), refreshTillTimeCapability: vi.fn(), posLockEvent: 'gomar-pos-lock',
}))

const id = '92000000-0000-4000-8000-000000000001'
const exact = `#entity=1&outlet=1&venta=${id}`
const grant: Grant = {
  actor: 3, entity: 1, outlet: 1, lease: 'active-lease', expires: Date.now() + 600_000,
  label: 'Centro', catalog: [{ id: 7, nombre: 'Pan', precio_centavos: 600, revision: 1, activo: true }],
}
const pending = draft(grant, new Map([[7, 1]]), id, Date.now(), 'efectivo')
const http = vi.fn()

beforeEach(() => {
  vi.stubGlobal('navigator', { onLine: true })
  vi.stubGlobal('fetch', http)
  vi.mocked(readGrant).mockResolvedValue(grant)
  vi.mocked(salesPage).mockResolvedValue({ sales: [pending], next: undefined })
  vi.mocked(pendingSalesFor).mockResolvedValue([pending])
  http.mockImplementation(async (url: string) => {
    if (url.endsWith('/entities')) return Response.json([{ id: 1, nombre: 'QA', rol: 'administration', capacidades: [] }])
    if (url.endsWith('/catalog')) return Response.json({ actor_id: 3, productos: grant.catalog })
    if (url.endsWith('/sales')) return Response.json({ id: 4, id_solicitud: id, total_centavos: 600, metodo_pago: 'efectivo' })
    if (url.endsWith('/auth/csrf') || url.endsWith('/auth/me')) return Response.json({})
    throw new Error('Unexpected request')
  })
})
afterEach(() => { vi.clearAllMocks(); vi.unstubAllGlobals() })

describe('entrada de Caja reservada para horarios', () => {
  it.each([
    { hash: '#entity=1&outlet=1&venta=incorrecta', search: '', emptyQuery: false },
    { hash: exact, search: '?outlet_id=1', emptyQuery: false },
    { hash: exact, search: '', emptyQuery: true },
    { hash: '', search: `?venta=${id}`, emptyQuery: false },
    { hash: '#entity=1&outlet=1&%76enta=%MAL', search: '', emptyQuery: false },
    { hash: '#entity=1%26outlet=1%26venta%3D', search: '', emptyQuery: false },
    { hash: '#entity=1&outlet=1&VENTA', search: '', emptyQuery: false },
  ])('deja el enlace inválido inerte con cola activa: %j', async (props) => {
    const panel = mount(PosRoute, { props })
    await flushPromises()
    expect(panel.get('h1').text()).toBe('Enlace de horarios no válido')
    expect(panel.find('button').exists()).toBe(false)
    expect(readGrant).not.toHaveBeenCalled()
    expect(salesPage).not.toHaveBeenCalled()
    expect(pendingSalesFor).not.toHaveBeenCalled()
    expect(http).not.toHaveBeenCalled()
    expect(recordOutcome).not.toHaveBeenCalled()
    panel.unmount()
  })

  it('conserva el enlace exacto en solo lectura hasta que se pulsa Consultar', async () => {
    const panel = mount(PosRoute, { props: { hash: exact, search: '', emptyQuery: false } })
    await flushPromises()
    expect(panel.get('button').text()).toBe('Consultar horarios')
    expect(readGrant).not.toHaveBeenCalled()
    expect(salesPage).not.toHaveBeenCalled()
    expect(pendingSalesFor).not.toHaveBeenCalled()
    expect(http).not.toHaveBeenCalled()
    expect(recordOutcome).not.toHaveBeenCalled()
    panel.unmount()
  })

  it.each(['', '#entity=1&outlet=1&table=7'])('conserva la Caja normal y de mesa: %s', async (hash) => {
    const panel = mount(PosRoute, { props: { hash, search: '', emptyQuery: false } })
    await flushPromises()
    expect(panel.get('h1').text()).toBe('Caja')
    expect(readGrant).toHaveBeenCalledWith(grant.lease, expect.any(Number))
    expect(http).toHaveBeenCalledWith('/api/v1/entities/1/outlets/1/pos/sales', expect.objectContaining({ method: 'POST' }))
    expect(recordOutcome).toHaveBeenCalledWith(expect.objectContaining({ actor: 3, entity: 1, outlet: 1, lease: grant.lease }), id, 'synced', 4, undefined)
    panel.unmount()
  })

  it('opens acknowledged pages without adding acknowledged or attention sales to sync', async () => {
    const recent = { ...pending, status: 'synced' as const, serverId: 3 }
    const older = { ...recent, id: '92000000-0000-4000-8000-000000000002', serverId: 2 }
    const attention = { ...pending, id: '92000000-0000-4000-8000-000000000003', status: 'attention' as const }
    vi.mocked(pendingSalesFor).mockResolvedValue([])
    vi.mocked(salesPage).mockImplementation(async (_scope, before) => before
      ? { sales: [older, attention], next: undefined }
      : { sales: [recent, attention], next: [recent.created, recent.id] })
    const panel = mount(PosRoute, { props: { hash: '', search: '', emptyQuery: false } })
    const button = (text: string) => panel.findAll('button').find((entry) => entry.text() === text)!
    await flushPromises()
    await button('Ventas sincronizadas anteriores').trigger('click')
    await flushPromises()
    expect(salesPage).toHaveBeenLastCalledWith(expect.objectContaining(grant), [recent.created, recent.id])
    expect(panel.text()).toContain('Necesita revisión')
    await button('Ventas recientes').trigger('click')
    await flushPromises()
    expect(salesPage).toHaveBeenLastCalledWith(expect.objectContaining(grant), undefined)
    expect(recordOutcome).not.toHaveBeenCalled()
    expect(http.mock.calls.some(([url]) => url.endsWith('/sales'))).toBe(false)
    panel.unmount()
  })
})
