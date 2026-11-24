import { draft, quoteCart, sameContext, scopeKey, type Context, type Grant, type PaymentMethod, type Sale } from './model'
import type { ReceiptClock } from './sale-time'
import type { TableSave } from './tableSave'

const dbName = 'gomar-pos-v1'
const tableSaveKey = (context: Context) => `table-save:${scopeKey(context)}`
const tableRecoveryKey = (context: Context) => `table-save-recovery:${scopeKey(context)}`
const leaseName = 'gomar-pos-active-lease'
export const posLockEvent = 'gomar-pos-lock'

export function localLease() {
  return sessionStorage.getItem(leaseName) ?? ''
}

export function rememberLease(value: string) {
  sessionStorage.setItem(leaseName, value)
}

export function forgetLease() {
  sessionStorage.removeItem(leaseName)
}

export function broadcastPOSLock() {
  try { localStorage.setItem(posLockEvent, crypto.randomUUID()) } catch { /* Storage may be unavailable. */ }
}

export function lockOfflinePOS() {
  try { forgetLease() } catch { /* Storage can be unavailable in a private window. */ }
  broadcastPOSLock()
}

function open(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(dbName, 1)
    req.onupgradeneeded = () => {
      req.result.createObjectStore('meta')
      req.result.createObjectStore('sales', { keyPath: 'id' }).createIndex('scope', 'key')
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
    req.onblocked = () => reject(new Error('Cerrá las otras pestañas de caja y volvé a intentar.'))
  })
}

function result<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

async function transact<T>(stores: string[], mode: IDBTransactionMode, run: (tx: IDBTransaction) => Promise<T>): Promise<T> {
  const db = await open()
  try {
    const tx = db.transaction(stores, mode, mode === 'readwrite' ? { durability: 'strict' } : undefined)
    const done = new Promise<void>((resolve, reject) => {
      tx.oncomplete = () => resolve()
      tx.onabort = () => reject(tx.error ?? new Error('No se pudieron guardar los datos de caja.'))
      tx.onerror = () => reject(tx.error)
    })
    try {
      const [value] = await Promise.all([run(tx), done])
      return value
    } catch (error) {
      if (mode === 'readwrite') {
        try { tx.abort() } catch { /* The transaction already completed or aborted. */ }
      }
      throw error
    }
  } finally {
    db.close()
  }
}

function armed(tx: IDBTransaction): Promise<Grant | undefined> {
  return result(tx.objectStore('meta').get('active'))
}

export function readGrant(lease: string, now: number) {
  if (!lease) return Promise.resolve(undefined)
  return transact(['meta'], 'readonly', async (tx) => {
    const value = await armed(tx)
    return value?.lease === lease && now < value.expires ? value : undefined
  })
}

export function arm(grant: Grant) {
  return transact(['meta'], 'readwrite', async (tx) => {
    await result(tx.objectStore('meta').put(grant, 'active'))
  })
}

export function refreshTillTimeCapability(grant: Grant, tillTimeV1: boolean) {
  return transact(['meta'], 'readwrite', async (tx) => {
    const current = await armed(tx)
    if (!current || current.lease !== grant.lease || !sameContext(current, grant)) return
    if ((current.tillTimeV1 === true) === tillTimeV1) return current
    const updated = { ...current, tillTimeV1 }
    await result(tx.objectStore('meta').put(updated, 'active'))
    return updated
  })
}

export function disarm(lease: string) {
  if (!lease) return Promise.resolve()
  return transact(['meta'], 'readwrite', async (tx) => {
    if ((await armed(tx))?.lease !== lease) return
    await result(tx.objectStore('meta').delete('active'))
  })
}

function tableSave(tx: IDBTransaction, context: Context): Promise<TableSave | undefined> {
  return result(tx.objectStore('meta').get(tableSaveKey(context)))
}

function tableRecovery(tx: IDBTransaction, context: Context): Promise<TableSave | undefined> {
  return result(tx.objectStore('meta').get(tableRecoveryKey(context)))
}

function sameRegister(grant: Context & { lease: string }, current: Grant | undefined) {
  return current?.lease === grant.lease && sameContext(current, grant)
}

export function readTableState(grant: Grant) {
  return transact(['meta'], 'readonly', async (tx) => {
    if (!sameRegister(grant, await armed(tx))) return {}
    const [pending, recovery] = await Promise.all([tableSave(tx, grant), tableRecovery(tx, grant)])
    return {
      pending: pending && sameContext(grant, pending) ? pending : undefined,
      recovery: recovery && sameContext(grant, recovery) ? recovery : undefined,
    }
  })
}

export function beginTableSave(grant: Grant, quantities: ReadonlyMap<number, number>, table: number, id: string, now: number) {
  return transact(['meta'], 'readwrite', async (tx) => {
    const current = await armed(tx)
    if (!current || !sameRegister(grant, current)) throw new Error('La caja se bloqueó.')
    if (await tableSave(tx, grant) || await tableRecovery(tx, grant)) throw new Error('Hay una cuenta pendiente de revisión.')
    const { actor, entity, outlet } = current
    const { lines, total } = quoteCart(current, quantities, now)
    const items = lines.map(({ producto_id, revision, cantidad, nombre }) => ({ producto_id, revision, cantidad, nombre }))
    const pending = { actor, entity, outlet, id, table, items, total }
    await result(tx.objectStore('meta').add(pending, tableSaveKey(grant)))
    return pending
  })
}

export function finishTableSave(grant: Grant, intent: TableSave, retainForReview = false) {
  return transact(['meta'], 'readwrite', async (tx) => {
    const stored = await tableSave(tx, grant)
    if (!sameContext(grant, intent) || !sameRegister(grant, await armed(tx)) || stored?.id !== intent.id) {
      throw new Error('La cuenta o la caja cambió.')
    }
    const meta = tx.objectStore('meta')
    // Never overwrite an unacknowledged summary or release the hold
    // without durably keeping the original snapshot.
    if (retainForReview) await result(meta.add(stored, tableRecoveryKey(grant)))
    await result(meta.delete(tableSaveKey(grant)))
  })
}

export function acknowledgeTableRecovery(grant: Grant, id: string) {
  return transact(['meta'], 'readwrite', async (tx) => {
    if (!sameRegister(grant, await armed(tx)) || (await tableRecovery(tx, grant))?.id !== id) return false
    await result(tx.objectStore('meta').delete(tableRecoveryKey(grant)))
    return true
  })
}

export function salesFor(context: Context) {
  return transact(['sales'], 'readonly', (tx) => result<Sale[]>(tx.objectStore('sales').index('scope').getAll(scopeKey(context))))
}

export function enqueue(grant: Grant, quantities: ReadonlyMap<number, number>, id: string, now: number, paymentMethod: PaymentMethod) {
  return transact(['meta', 'sales'], 'readwrite', async (tx) => {
    const current = await armed(tx)
    if (!current || !sameRegister(grant, current)) throw new Error('La caja se bloqueó.')
    if (await tableSave(tx, grant)) throw new Error('Hay una cuenta sin verificar.')
    const sale = draft(current, quantities, id, now, paymentMethod)
    await result(tx.objectStore('sales').add(sale))
    return sale
  })
}

export function recordOutcome(context: Context, id: string, status: Sale['status'], serverId?: number, receiptClock?: ReceiptClock) {
  return transact(['sales'], 'readwrite', async (tx) => {
    const store = tx.objectStore('sales')
    const sale = await result<Sale | undefined>(store.get(id))
    if (!sale || !sameContext(context, sale) || sale.status !== 'pending') return
    await result(store.put({ ...sale, status, serverId, receiptClock }))
  })
}
