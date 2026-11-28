import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import PosApp from '../src/pos/PosApp.vue'
import SavedTableCheck from '../src/pos/SavedTableCheck.vue'
import { quoteCart, sameContext, scopeKey, type Grant } from '../src/pos/model'
import type { TableSave } from '../src/pos/tableSave'
import { acknowledgeTableRecovery, arm, beginTableSave, enqueue, finishTableSave, forgetLease, localLease, lockOfflinePOS, readGrant, readTableState, rememberLease } from '../src/pos/storage'

vi.mock('../src/pos/storage', () => ({
  localLease: vi.fn(), readGrant: vi.fn(), salesPage: vi.fn(async () => ({ sales: [] })), pendingSalesFor: vi.fn(async () => []),
  beginTableSave: vi.fn(), finishTableSave: vi.fn(), readTableState: vi.fn(), acknowledgeTableRecovery: vi.fn(),
  enqueue: vi.fn(), forgetLease: vi.fn(), disarm: vi.fn(), lockOfflinePOS: vi.fn(),
  arm: vi.fn(), rememberLease: vi.fn(), broadcastPOSLock: vi.fn(),
  posLockEvent: 'gomar-pos-lock',
}))

const products = [
  { id: 7, nombre: 'Pan', revision: 4, precio_centavos: 750, activo: true, codigo_barras: '0007' },
  { id: 8, nombre: 'Café', revision: 2, precio_centavos: 750, activo: true },
]
let grant: Grant
let lease: string
let actor: number
let catalog: Grant['catalog']
const pending = new Map<string, TableSave>()
const summaries = new Map<string, TableSave>()
const originKey = '3:1:2'
const transfer = () => pending.get(originKey)
let app: VueWrapper | undefined
let replies: Array<() => Promise<Response>>
let sends: unknown[]
const answer = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status }))
const button = (text: string) => app!.findAll('button').find((b) => b.text() === text)!
const add = (product: string) => app!.get(`[aria-label="Agregar ${product}"]`)
const pay = () => app!.get('select')
const table = () => app!.get('input[inputmode=numeric]')
const receipt = (estado = 'sin_cobro') => ({
  id_cuenta: '42', id_solicitud: transfer()?.id, estado, mesa: 17, total_centavos: 750,
  items: [{ producto_id: 7, revision: 4, cantidad: 1, nombre: 'Pan', precio_centavos: 750 }],
})
const refusal = (detail: string, rejection = 'mesa_ocupada') =>
  answer({ detail, id_solicitud: transfer()?.id, mesa: 17, rechazo: rejection }, 409)

beforeEach(() => {
  window.history.replaceState(null, '', '/pos')
  grant = { actor: 3, entity: 1, outlet: 2, lease: 'local-register', label: 'Centro', expires: Date.now() + 60_000,
    catalog: products, tableChecks: true }
  actor = 3
  lease = grant.lease
  catalog = products
  pending.clear()
  summaries.clear()
  sends = []
  replies = []
  vi.stubGlobal('isSecureContext', true)
  vi.stubGlobal('navigator', {
    onLine: true,
    serviceWorker: { controller: {}, addEventListener: vi.fn(), register: vi.fn(async () => ({})) },
  })
  vi.mocked(localLease).mockImplementation(() => lease)
  vi.mocked(rememberLease).mockImplementation((opened) => { lease = opened })
  vi.mocked(forgetLease).mockImplementation(() => { lease = '' })
  vi.mocked(lockOfflinePOS).mockImplementation(() => { lease = '' })
  vi.mocked(arm).mockImplementation(async (armed) => { grant = armed })
  vi.mocked(readGrant).mockImplementation(async (requested) => requested === grant.lease && Date.now() < grant.expires ? grant : undefined)
  vi.mocked(readTableState).mockImplementation(async (scope) => scope.lease === grant.lease && sameContext(scope, grant)
    ? { pending: pending.get(scopeKey(scope)), recovery: summaries.get(scopeKey(scope)) } : {})
  vi.mocked(beginTableSave).mockImplementation(async (scope, basket, table, id, now) => {
    const { actor, entity, outlet } = scope
    const { lines, total } = quoteCart(scope, basket, now)
    const items = lines.map(({ producto_id, revision, cantidad, nombre }) => ({ producto_id, revision, cantidad, nombre }))
    const intent = { actor, entity, outlet, id, table, items, total }
    pending.set(scopeKey(scope), intent)
    return intent
  })
  vi.mocked(finishTableSave).mockImplementation(async (scope, intent, retain) => {
    if (scope.lease !== grant.lease || !sameContext(scope, intent) || pending.get(scopeKey(scope))?.id !== intent.id) throw new Error('register changed')
    if (retain) summaries.set(scopeKey(scope), intent)
    pending.delete(scopeKey(scope))
  })
  vi.mocked(acknowledgeTableRecovery).mockImplementation(async (scope, id) => {
    if (scope.lease !== grant.lease || summaries.get(scopeKey(scope))?.id !== id) return false
    summaries.delete(scopeKey(scope))
    return true
  })
  vi.stubGlobal('fetch', vi.fn((url: string, options: RequestInit) => {
    if (url.endsWith('/entities')) return answer([{ id: 1, nombre: 'QA', rol: 'administration', capacidades: [] }])
    if (url.endsWith('/outlets')) return answer([{ id: 2, nombre: 'Centro' }, { id: 3, nombre: 'Otro local' }])
    if (url.endsWith('/catalog')) return answer({ actor_id: actor, cuentas_mesa: true, productos: catalog })
    if (url.endsWith('/table-checks') && options.method === 'POST') {
      sends.push(JSON.parse(options.body as string))
      const next = replies.shift()
      if (!next) throw new Error('Unexpected table POST')
      return next()
    }
    if (url.endsWith('/auth/me')) return actor ? answer({}) : answer({}, 401)
    if (url.endsWith('/auth/csrf')) return answer({})
    throw new Error(`Unexpected request: ${url}`)
  }))
})

afterEach(() => {
  app?.unmount()
  app = undefined
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

async function open() {
  app = mount(PosApp)
  await flushPromises()
}

async function start() {
  await open()
  await add('Pan').trigger('click')
  await pay().setValue('efectivo')
  await button('Ver cuenta de mesa').trigger('click')
  await table().setValue('17')
  await button('Guardar cuenta sin cobro').trigger('click')
  await flushPromises()
}

function expectLocked() {
  expect(button('Registrar venta').attributes()).toHaveProperty('disabled')
  expect(add('Café').attributes()).toHaveProperty('disabled')
  expect(pay().attributes()).toHaveProperty('disabled')
  expect(table().attributes()).toHaveProperty('disabled')
  expect(button('Consultar mesa').attributes()).toHaveProperty('disabled')
  expect(button('Ver cuenta de mesa').attributes()).toHaveProperty('disabled')
  expect(app!.find('.pos-prebill-preview').exists()).toBe(false)
  expect(app!.text()).toContain('Pan × 1')
  expect(enqueue).not.toHaveBeenCalled()
}

it('locks every cart action before awaiting the save and consumes only the server-confirmed cart', async () => {
  let deliver!: (value: Response) => void
  replies.push(() => new Promise((resolve) => { deliver = resolve }))
  await start()
  expectLocked()
  // A disabled UI is not the only guard.
  await button('Registrar venta').trigger('click')
  await add('Café').trigger('click')
  window.history.replaceState(null, '', '/pos#entity=1&outlet=2&table=29')
  window.dispatchEvent(new Event('hashchange'))
  expect((table().element as HTMLInputElement).value).toBe('17')
  expect(sends).toEqual([{ id_solicitud: transfer()!.id, actor_id: 3, mesa: 17, items: [{ producto_id: 7, revision: 4, cantidad: 1 }] }])
  deliver(await answer(receipt(), 201))
  await flushPromises()
  expect(app!.text()).toContain('Cuenta guardada: mesa 17')
  expect(app!.text()).not.toContain('Pan × 1')
  expect((pay().element as HTMLSelectElement).value).toBe('')
  expect(button('Registrar venta').attributes()).toHaveProperty('disabled')
  expect(button('Guardar cuenta sin cobro').attributes()).toHaveProperty('disabled')
  expect(transfer()).toBeUndefined()
  await add('Café').trigger('click')
  await pay().setValue('efectivo')
  expect(button('Registrar venta').attributes()).not.toHaveProperty('disabled')
})

it('keeps a first definitively rejected cart and its payment choice available', async () => {
  replies.push(() => refusal('La mesa ya está ocupada.'))
  await start()
  expect(app!.text()).toContain('La mesa ya está ocupada.')
  expect(app!.text()).toContain('Pan × 1')
  expect((pay().element as HTMLSelectElement).value).toBe('efectivo')
  expect(button('Registrar venta').attributes()).not.toHaveProperty('disabled')
  expect(transfer()).toBeUndefined()
})

it.each(['finalizada', 'retirada'])('holds on mismatched success or rejected retry, reopens the exact intent and consumes it on a %s replay', async (state) => {
  replies.push(
    () => answer({ ...receipt(), items: [{ ...receipt().items[0], producto_id: 8 }] }, 201),
    () => answer({ detail: 'No se pudo consultar.' }, 409),
    () => answer(receipt(state)),
  )
  await start()
  expectLocked()
  expect(app!.text()).toContain('No se pudo verificar la cuenta de la mesa 17.')
  app!.unmount()
  window.history.replaceState(null, '', '/pos#entity=1&outlet=2&table=29')
  await open()
  expect((table().element as HTMLInputElement).value).toBe('17')
  expectLocked()
  await button('Reintentar guardar la misma cuenta').trigger('click')
  await flushPromises()
  expectLocked()
  await button('Reintentar guardar la misma cuenta').trigger('click')
  await flushPromises()
  expect(sends).toHaveLength(3)
  expect(sends.every((item) => JSON.stringify(item) === JSON.stringify(sends[0]))).toBe(true)
  expect(app!.text()).toContain(state === 'finalizada' ? 'Venta registrada: mesa 17' : 'Cuenta retirada: mesa 17')
  expect(app!.text()).not.toContain('Pan × 1')
  expect(transfer()).toBeUndefined()
  expect(enqueue).not.toHaveBeenCalled()
})

it('does not apply a late save acknowledgment after losing the exact lease', async () => {
  let deliver!: (value: Response) => void
  replies.push(() => new Promise((resolve) => { deliver = resolve }))
  await start()
  lease = 'other-register'
  deliver(await answer(receipt(), 201))
  await flushPromises()
  expect(finishTableSave).not.toHaveBeenCalled()
  expect(app!.text()).not.toContain('Cuenta guardada: mesa 17')
  expectLocked()
})

const rearm = async () => {
  await button('Habilitar caja en esta pestaña').trigger('click')
  await flushPromises()
}

it('moves only the hold across lock/rearm; an old in-flight reply cannot unlock a newer exact retry', async () => {
  let first!: (value: Response) => void
  let replay!: (value: Response) => void
  replies.push(() => new Promise((resolve) => { first = resolve }), () => new Promise((resolve) => { replay = resolve }))
  await start()
  const oldLease = lease
  await button('Bloquear caja').trigger('click')
  await flushPromises()
  expect(app!.text()).not.toContain('Pan × 1')
  await rearm()
  expect(lease).not.toBe(oldLease)
  expectLocked()
  await button('Reintentar guardar la misma cuenta').trigger('click')
  await flushPromises()
  expect(sends[0]).toEqual(sends[1])
  first(await answer(receipt(), 201))
  await flushPromises()
  expect(app!.text()).toContain('Guardando la cuenta de mesa…')
  expect(finishTableSave).not.toHaveBeenCalled()
  expectLocked()
  replay(await answer(receipt()))
  await flushPromises()
  expect(app!.text()).toContain('Agregá productos para empezar.')
  expect(transfer()).toBeUndefined()
})

it('does not expose the hold after sign-out or to another cashier or outlet, but the original scoped cashier can resolve it', async () => {
  replies.push(() => Promise.reject(new TypeError('lost')), () => refusal('Mesa ocupada.'))
  await start()
  expectLocked()
  actor = 0
  window.dispatchEvent(new StorageEvent('storage', { key: 'gomar-pos-lock' }))
  window.dispatchEvent(new Event('online'))
  await flushPromises()
  expect(app!.text()).not.toContain('Pan × 1')
  actor = 9
  window.dispatchEvent(new Event('online'))
  await flushPromises()
  await rearm()
  expect(app!.text()).not.toContain('No se pudo verificar la cuenta')
  expect(app!.text()).not.toContain('Pan × 1')
  expect(sends).toHaveLength(1)
  await button('Bloquear caja').trigger('click')
  actor = 3
  await flushPromises()
  await app!.findAll('select')[1].setValue('3')
  await rearm()
  expect(app!.text()).not.toContain('No se pudo verificar la cuenta')
  expect(app!.text()).not.toContain('Pan × 1')
  await button('Bloquear caja').trigger('click')
  await flushPromises()
  await rearm()
  expectLocked()
  await button('Reintentar guardar la misma cuenta').trigger('click')
  await flushPromises()
  expect(sends[0]).toEqual(sends[1])
  expect(app!.text()).toContain('Mesa ocupada.')
  expect(app!.text()).toContain('Pan × 1')
  expect(transfer()).toBeUndefined()
})

it('retains rejected old-price products read-only across expiry, scope changes and reload until explicit acknowledgment', async () => {
  vi.useFakeTimers()
  replies.push(() => Promise.reject(new TypeError('lost')), () => refusal('El producto cambió.', 'catalogo_cambio'))
  await start()
  const previous = lease
  catalog = [{ ...products[0], nombre: 'Pan nuevo', revision: 5, precio_centavos: 880 }, products[1]]
  await vi.advanceTimersByTimeAsync(60_001)
  await flushPromises()
  expect(app!.text()).not.toContain('Pan × 1')
  await rearm()
  expect(lease).not.toBe(previous)
  expect(app!.text()).toContain('Pan × 1')
  expect(button('Registrar venta').attributes()).toHaveProperty('disabled')
  await button('Reintentar guardar la misma cuenta').trigger('click')
  await flushPromises()
  expect(sends[0]).toEqual(sends[1])
  expect(app!.text()).toContain('No se restauró el carrito anterior.')
  expect(app!.text()).toContain('Agregá productos para empezar.')
  expect(transfer()).toBeUndefined()
  expect(app!.get('[aria-label="Productos del carrito anterior"]').text()).toBe('Pan × 1')
  expect(button('Registrar venta').attributes()).toHaveProperty('disabled')
  app!.unmount()
  await open()
  expect(app!.get('[aria-label="Productos del carrito anterior"]').text()).toBe('Pan × 1')
  await button('Bloquear caja').trigger('click')
  actor = 9
  await flushPromises()
  await rearm()
  expect(app!.find('[aria-label="Productos del carrito anterior"]').exists()).toBe(false)
  await button('Bloquear caja').trigger('click')
  actor = 3
  await flushPromises()
  await rearm()
  expect(app!.get('[aria-label="Productos del carrito anterior"]').text()).toBe('Pan × 1')
  await add('Café').trigger('click')
  await table().setValue('17')
  await pay().setValue('efectivo')
  expect(button('Guardar cuenta sin cobro').attributes()).toHaveProperty('disabled')
  expect(button('Registrar venta').attributes()).not.toHaveProperty('disabled')
  expect(app!.text()).toContain('Café × 1')
  await button('Marcar lista como revisada').trigger('click')
  await flushPromises()
  expect(app!.find('[aria-label="Productos del carrito anterior"]').exists()).toBe(false)
  expect(app!.text()).toContain('Café × 1')
  expect(button('Guardar cuenta sin cobro').attributes()).not.toHaveProperty('disabled')
  expect(sends).toHaveLength(2)
  expect(enqueue).not.toHaveBeenCalled()
  expect(summaries.size).toBe(0)
})

it('offers retirement only after refreshing a successful tender correction', async () => {
  const entry = { id_registro: '52', tipo: 'record', medio: 'efectivo', importe_centavos: 500 }
  let corrected = false
  vi.stubGlobal('fetch', vi.fn((url: string, options: RequestInit) => {
    if (url.endsWith('/auth/csrf')) return answer({})
    if (url.endsWith('/table-checks/17')) return answer(receipt('con_registros'))
    if (url.endsWith('/table-checks/17/tender')) {
      if (options.method === 'POST') {
        corrected = true
        return answer({ id_registro: '53', id_cuenta: '42' }, 201)
      }
      return answer({
        id_cuenta: '42', registrado_centavos: corrected ? 0 : 500, saldo_centavos: corrected ? 750 : 250,
        registros: corrected ? [entry, { id_registro: '53', tipo: 'void', registro_original: '52', motivo: 'entrada_incorrecta' }] : [entry],
      })
    }
    if (url.endsWith('/table-checks/17/retire')) {
      expect(JSON.parse(options.body as string)).toEqual({ id_cuenta: '42', motivo: 'cancelled' })
      return answer(receipt('retirada'))
    }
    throw new Error(`Unexpected request: ${url}`)
  }))
  app = mount(SavedTableCheck, { props: {
    scope: { actor: 3, entity: 1, outlet: 2 }, online: true, allowTender: true, allowFinalization: false,
    allowed: () => true, formatMoney: String, cartReady: false, cartEmpty: true, save: async () => undefined,
  } })
  await table().setValue('17')
  await button('Consultar mesa').trigger('click')
  await flushPromises()
  expect(button('Retirar cuenta sin cobro')).toBeUndefined()
  await button('Corregir registro').trigger('click')
  await app.get('form select').setValue('entrada_incorrecta')
  await app.get('form').trigger('submit')
  await flushPromises()
  expect(app.text()).toContain('Corrección: Registro por error')
  await button('Retirar cuenta sin cobro').trigger('click')
  await app.get('fieldset select').setValue('cancelled')
  await button('Confirmar retiro').trigger('click')
  await flushPromises()
  expect(app.text()).toContain('Cuenta retirada: mesa 17')
})
