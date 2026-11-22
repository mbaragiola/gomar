import { draft, sameContext, scopeKey, type Context, type Grant, type PaymentMethod, type Sale } from './model'

const dbName = 'gomar-pos-v1'
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

export function disarm(lease: string) {
  if (!lease) return Promise.resolve()
  return transact(['meta'], 'readwrite', async (tx) => {
    if ((await armed(tx))?.lease === lease) await result(tx.objectStore('meta').delete('active'))
  })
}

export function salesFor(context: Context) {
  return transact(['sales'], 'readonly', (tx) => result<Sale[]>(tx.objectStore('sales').index('scope').getAll(scopeKey(context))))
}

export function enqueue(grant: Grant, quantities: ReadonlyMap<number, number>, id: string, now: number, paymentMethod: PaymentMethod) {
  return transact(['meta', 'sales'], 'readwrite', async (tx) => {
    const current = await armed(tx)
    if (!current || current.lease !== grant.lease || !sameContext(current, grant)) throw new Error('La caja se bloqueó.')
    const sale = draft(current, quantities, id, now, paymentMethod)
    await result(tx.objectStore('sales').add(sale))
    return sale
  })
}

export function recordOutcome(context: Context, id: string, status: Sale['status'], serverId?: number) {
  return transact(['sales'], 'readwrite', async (tx) => {
    const store = tx.objectStore('sales')
    const sale = await result<Sale | undefined>(store.get(id))
    if (!sale || !sameContext(context, sale) || sale.status !== 'pending') return
    await result(store.put({ ...sale, status, serverId }))
  })
}
