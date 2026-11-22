<script setup lang="ts">
import ThemeSelect from '../components/ThemeSelect.vue'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, request } from '../api'
import { localDateTime, money } from '../format'
import { maxLines, maxQuantity, normalizedProducts, positive, sameContext, saleRequest, grantLifetime,
  isPaymentMethod, matchesPaymentMethod, paymentLabels,
  type Context, type Grant, type PaymentMethod, type Product, type Sale } from './model'
import { arm, broadcastPOSLock, disarm, enqueue, forgetLease, localLease, lockOfflinePOS, posLockEvent, readGrant, recordOutcome, rememberLease, salesFor } from './storage'

interface Entity { id: number; nombre: string; rol: string; capacidades: string[] }
interface Outlet { id: number; nombre: string }
interface Catalog { actor_id: number; productos: Product[] }
interface Acknowledgment { id: number; id_solicitud: string; total_centavos: number; metodo_pago?: PaymentMethod }
const statusLabel = { pending: 'Pendiente de sincronizar', synced: 'Sincronizada', attention: 'Necesita revisión' }
const sellingRoles = new Set(['administration', 'purchasing', 'accounting'])
const online = ref(navigator.onLine)
const navigatorOnline = ref(navigator.onLine)
const workerReady = ref(false)
const verifying = ref(false)
const changing = ref(false)
const syncing = ref(false)
const saving = ref(false)
const auth = ref(false)
const entities = ref<Entity[]>([])
const outlets = ref<Outlet[]>([])
const entity = ref(0)
const outlet = ref(0)
const grant = ref<Grant | null>(null)
const recorded = ref<Sale[]>([])
const basket = ref(new Map<number, number>())
const payment = ref<PaymentMethod | ''>('')
const notice = ref('')
const error = ref('')
const needsConfirm = ref(false)
let epoch = 0
let refreshTimer: ReturnType<typeof setInterval> | undefined

const canSell = computed(() => sellingRoles.has(entities.value.find((row) => row.id === entity.value)?.rol ?? ''))
const basketLines = computed(() => [...basket.value.entries()].flatMap(([id, quantity]) => {
  const row = grant.value?.catalog.find((item) => item.id === id)
  return row ? [{ row, quantity }] : []
}))
const total = computed(() => basketLines.value.reduce((sum, { row, quantity }) => sum + row.precio_centavos * quantity, 0))
const pending = computed(() => recorded.value.filter((sale) => sale.status === 'pending').length)
const fmt = (amount: number) => money(`${Math.trunc(amount / 100)}.${String(amount % 100).padStart(2, '0')}`, 'ARS')
const endpoint = (s: Pick<Context, 'entity' | 'outlet'>) => `/entities/${s.entity}/outlets/${s.outlet}/pos`

class ConnectionUnavailable extends Error {}

async function appRequest<T = unknown>(path: string, options: RequestInit = {}): Promise<T> {
  try {
    return await request<T>(path, { ...options, cache: 'no-store', signal: AbortSignal.timeout(4000) })
  } catch (err) {
    // Treat only a transport failure as offline; HTTP responses and malformed
    // payloads are not permission to open a cached shift.
    if (err instanceof TypeError || (err instanceof DOMException && err.name === 'TimeoutError')) {
      throw new ConnectionUnavailable()
    }
    throw err
  }
}

function clearView() {
  epoch++
  grant.value = null
  recorded.value = []
  basket.value = new Map()
  payment.value = ''
  needsConfirm.value = false
  notice.value = ''
}

function tell(errorValue: unknown, fallback: string) {
  error.value = errorValue instanceof ApiError ? errorValue.message : fallback
}

async function locked(message = '', broadcast = false) {
  const lease = localLease()
  if (broadcast) lockOfflinePOS()
  else forgetLease()
  clearView()
  error.value = message
  try { await disarm(lease) } catch { /* UI remains locked; the local lease is gone. */ }
}

async function scopedCatalog(s: Pick<Context, 'entity' | 'outlet'>): Promise<Catalog> {
  const catalog = await appRequest<Catalog>(`${endpoint(s)}/catalog`)
  if (!positive(catalog.actor_id)) throw new Error('No se pudo verificar la cuenta.')
  return catalog
}

async function selectEntity() {
  const token = epoch
  const id = entity.value
  outlets.value = []
  outlet.value = 0
  if (!online.value || !id) return
  try {
    const list = await appRequest<Outlet[]>(`/entities/${id}/outlets`)
    if (token !== epoch || id !== entity.value) return
    outlets.value = list
    outlet.value = list[0]?.id ?? 0
  } catch (err) {
    if (token === epoch) tell(err, 'No se pudieron cargar los locales.')
  }
}

async function openCached(current: Grant | undefined, token: number) {
  online.value = false
  auth.value = false
  entities.value = []
  outlets.value = []
  if (!current) return
  const sales = await salesFor(current)
  if (token === epoch && localLease() === current.lease && Date.now() < current.expires) {
    grant.value = current
    recorded.value = sales
  }
}

async function refresh() {
  if (verifying.value || changing.value || saving.value) return
  const token = epoch
  verifying.value = true
  navigatorOnline.value = navigator.onLine
  let current: Grant | undefined
  try {
    const lease = localLease()
    current = await readGrant(lease, Date.now())
    if (token !== epoch) return
    if (lease && !current) {
      await locked('Venció o se cerró el turno local. Las ventas guardadas se conservan.')
      return
    }
    if (!navigatorOnline.value) {
      await openCached(current, token)
      return
    }
    await appRequest('/auth/csrf')
    await appRequest('/auth/me')
    const authorized = await appRequest<Entity[]>('/entities')
    if (token !== epoch || !navigator.onLine) return
    online.value = true
    entities.value = authorized
    auth.value = true
    error.value = ''
    if (current) {
      const member = authorized.find((row) => row.id === current?.entity)
      if (!member || !sellingRoles.has(member.rol)) {
        await locked('La cuenta actual no puede usar esta caja.')
        return
      }
      const catalog = await scopedCatalog(current)
      if (token !== epoch || !navigator.onLine) return
      if (catalog.actor_id !== current.actor) {
        await locked('Cambió la cuenta. Las ventas de la otra cuenta se conservan sin sincronizar.')
        return
      }
      const sales = await salesFor(current)
      if (token !== epoch || localLease() !== current.lease) return
      grant.value = current
      recorded.value = sales
    } else {
      entity.value = authorized.find((row) => sellingRoles.has(row.rol))?.id ?? authorized[0]?.id ?? 0
      await selectEntity()
    }
  } catch (err) {
    if (token !== epoch) return
    auth.value = false
    if (err instanceof ConnectionUnavailable) {
      try { await openCached(current, token) }
      catch { clearView(); error.value = 'No se pudo abrir la caja guardada.' }
      return
    }
    if (err instanceof ApiError && (err.status === 401 || err.status === 403 || err.status === 404)) {
      await locked('Ingresá con la cuenta autorizada para usar esta caja.')
    } else {
      clearView()
      tell(err, 'No se pudo verificar la cuenta. Si estás sin conexión, usá la caja ya habilitada de este turno.')
    }
  } finally {
    verifying.value = false
  }
  if (current && online.value && grant.value?.lease === current.lease) void sync()
}

async function enable() {
  if (!online.value || !auth.value || !canSell.value || !entity.value || !outlet.value || changing.value || !workerReady.value) return
  changing.value = true
  error.value = ''
  const selection = { entity: entity.value, outlet: outlet.value }
  const label = outlets.value.find((row) => row.id === selection.outlet)?.nombre ?? 'Local'
  const token = epoch
  try {
    const catalog = await scopedCatalog(selection)
    if (token !== epoch || !navigator.onLine) return
    const next: Grant = { ...selection, actor: catalog.actor_id, label, lease: crypto.randomUUID(),
      catalog: normalizedProducts(catalog.productos), expires: Date.now() + grantLifetime }
    await arm(next)
    if (token !== epoch) {
      await disarm(next.lease)
      return
    }
    rememberLease(next.lease)
    broadcastPOSLock()
    const sales = await salesFor(next)
    if (token !== epoch) return
    grant.value = next
    recorded.value = sales
    notice.value = 'Caja habilitada en esta pestaña por hasta 8 horas.'
  } catch (err) {
    tell(err, 'No se pudo habilitar la caja.')
  } finally {
    changing.value = false
  }
  if (grant.value) void sync()
}

function quantity(id: number, step: number) {
  if (!grant.value || saving.value || needsConfirm.value) return
  const next = (basket.value.get(id) ?? 0) + step
  if (next > maxQuantity || (next > 0 && !basket.value.has(id) && basket.value.size >= maxLines)) return
  if (next <= 0) basket.value.delete(id)
  else basket.value.set(id, next)
}

async function complete() {
  const current = grant.value
  const method = payment.value
  if (!current || !isPaymentMethod(method) || !needsConfirm.value || saving.value || verifying.value || localLease() !== current.lease) return
  saving.value = true
  error.value = ''
  notice.value = ''
  const token = epoch
  try {
    const sale = await enqueue(current, new Map(basket.value), crypto.randomUUID(), Date.now(), method)
    if (token !== epoch) return
    recorded.value = [...recorded.value, sale]
    basket.value = new Map()
    payment.value = ''
    needsConfirm.value = false
    notice.value = 'Venta guardada en este dispositivo. No es un comprobante fiscal.'
  } catch (err) {
    tell(err, 'No se guardó la venta. Revisá la caja y volvé a intentar.')
  } finally {
    saving.value = false
  }
  if (online.value) void sync()
}

async function sync() {
  const current = grant.value
  if (!current || syncing.value || verifying.value || !navigator.onLine || localLease() !== current.lease) return
  syncing.value = true
  const token = epoch
  try {
    const authorized = await appRequest<Entity[]>('/entities')
    if (token !== epoch || !navigator.onLine || localLease() !== current.lease) return
    if (!sellingRoles.has(authorized.find((row) => row.id === current.entity)?.rol ?? '')) {
      await locked('La cuenta actual no puede registrar ventas.')
      return
    }
    const fresh = await scopedCatalog(current)
    if (token !== epoch || !navigator.onLine || localLease() !== current.lease) return
    if (fresh.actor_id !== current.actor) {
      await locked('Cambió la cuenta. Las ventas guardadas no se enviaron.')
      return
    }
    const sales = await salesFor(current)
    for (const sale of sales.sort((a, b) => a.created - b.created || a.id.localeCompare(b.id))) {
      if (token !== epoch || !navigator.onLine || localLease() !== current.lease || Date.now() >= current.expires) break
      if (sale.status !== 'pending' || !sameContext(current, sale)) continue
      try {
        const accepted = await appRequest<Acknowledgment>(`${endpoint(current)}/sales`, {
          method: 'POST', body: JSON.stringify(saleRequest(sale)),
        })
        if (accepted.id_solicitud !== sale.id || accepted.total_centavos !== sale.total || !positive(accepted.id)
          || !matchesPaymentMethod(sale, accepted.metodo_pago)) {
          await recordOutcome(current, sale.id, 'attention')
        } else {
          await recordOutcome(current, sale.id, 'synced', accepted.id)
        }
      } catch (err) {
        if (err instanceof ApiError && (err.status === 409 || err.status === 422)) {
          await recordOutcome(current, sale.id, 'attention')
          continue
        }
        throw err
      }
    }
    if (token === epoch) {
      const sales = await salesFor(current)
      if (token === epoch) recorded.value = sales
    }
  } catch (err) {
    if (token !== epoch) return
    if (err instanceof ApiError && (err.status === 401 || err.status === 403 || err.status === 404)) {
      await locked('Se perdió el acceso. Las ventas quedan guardadas sin sincronizar.')
    } else {
      if (err instanceof ConnectionUnavailable) online.value = false
      error.value = 'No se pudo sincronizar. Las ventas pendientes quedan guardadas.'
    }
  } finally {
    syncing.value = false
  }
}

function connection() {
  navigatorOnline.value = navigator.onLine
  if (!navigatorOnline.value) online.value = false
  void refresh()
}

function visibility() {
  if (document.visibilityState === 'visible') void refresh()
}

function otherTab(event: StorageEvent) {
  if (event.key === posLockEvent) void locked('Se cerró el turno desde otra pestaña.')
}

onMounted(async () => {
  window.addEventListener('online', connection)
  window.addEventListener('offline', connection)
  window.addEventListener('storage', otherTab)
  document.addEventListener('visibilitychange', visibility)
  refreshTimer = setInterval(() => {
    if (grant.value && Date.now() >= grant.value.expires) void locked('Venció el turno local. Las ventas guardadas se conservan.')
    else if (grant.value && !online.value && navigator.onLine && document.visibilityState === 'visible') void refresh()
  }, 15000)
  void refresh()
  if ('serviceWorker' in navigator && window.isSecureContext) {
    if (navigator.serviceWorker.controller) workerReady.value = true
    navigator.serviceWorker.addEventListener('controllerchange', () => { workerReady.value = !!navigator.serviceWorker.controller }, { once: true })
    if (navigator.onLine) {
      try { await navigator.serviceWorker.register('/pos-sw.js', { scope: '/pos' }) }
      catch { error.value = 'No se pudo preparar esta pestaña para trabajar sin conexión.' }
    }
  }
})

onUnmounted(() => {
  window.removeEventListener('online', connection)
  window.removeEventListener('offline', connection)
  window.removeEventListener('storage', otherTab)
  document.removeEventListener('visibilitychange', visibility)
  clearInterval(refreshTimer)
})
</script>

<template>
  <main class="workspace pos-workspace">
    <header class="section-heading">
      <div><p class="eyebrow">GOMAR</p><h1>Caja</h1></div>
      <ThemeSelect />
      <span role="status">{{ online ? 'Con conexión' : 'Sin conexión' }}</span>
    </header>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <button v-if="!online && navigatorOnline" type="button" :disabled="verifying || syncing" @click="refresh">Reintentar conexión</button>
    <section v-if="!grant || (online && verifying)" class="card gomar-feature">
      <h2>Habilitá esta caja</h2>
      <p>Usá un dispositivo de confianza y esta misma pestaña. Bloqueá la caja antes de compartir el equipo.</p>
      <p v-if="verifying">Verificando cuenta…</p>
      <template v-else-if="auth && online">
        <label>Entidad
          <select v-model="entity" :disabled="changing" @change="selectEntity">
            <option v-for="item in entities" :key="item.id" :value="item.id">{{ item.nombre }}</option>
          </select>
        </label>
        <label>Local
          <select v-model="outlet" :disabled="changing">
            <option v-for="item in outlets" :key="item.id" :value="item.id">{{ item.nombre }}</option>
          </select>
        </label>
        <p v-if="!workerReady" role="status">Preparando la caja para usarla sin conexión…</p>
        <p v-if="!canSell">Tu rol no permite registrar ventas en este local.</p>
        <button class="primary" :disabled="changing || !outlet || !workerReady || !canSell" @click="enable">Habilitar caja en esta pestaña</button>
      </template>
      <p v-else-if="!online">Conectate para habilitar una caja. No se abren turnos nuevos sin conexión.</p>
      <a v-else href="/">Ingresá al espacio de trabajo</a>
    </section>
    <template v-else>
      <section class="card gomar-feature">
        <div class="section-heading"><h2>{{ grant.label }}</h2><button type="button" @click="locked('Caja bloqueada.', true)">Bloquear caja</button></div>
        <p>{{ pending }} ventas pendientes · Catálogo guardado al habilitar la caja.</p>
        <p v-if="verifying" role="status">Verificando la cuenta…</p>
        <ul class="gomar-feature-list" aria-label="Catálogo">
          <li v-for="item in grant.catalog" :key="item.id" class="pos-catalog-row">
            <span>{{ item.nombre }} <small>{{ fmt(item.precio_centavos) }}</small></span>
            <button type="button" :aria-label="`Agregar ${item.nombre}`" :disabled="saving || verifying || needsConfirm" @click="quantity(item.id, 1)">Agregar</button>
          </li>
        </ul>
        <p v-if="!grant.catalog.length">No hay productos activos en el catálogo de este local.</p>
      </section>
      <section class="card gomar-feature">
        <h2>Venta actual</h2>
        <p v-if="!basketLines.length">Agregá productos para empezar.</p>
        <ul v-else class="gomar-feature-list">
          <li v-for="{ row, quantity: count } in basketLines" :key="row.id" class="pos-catalog-row">
            <span>{{ row.nombre }} × {{ count }}</span>
            <button type="button" :aria-label="`Quitar ${row.nombre}`" :disabled="saving || verifying || needsConfirm" @click="quantity(row.id, -1)">Quitar uno</button>
          </li>
        </ul>
        <strong>Total: {{ fmt(total) }}</strong>
        <label>Medio de pago
          <select v-model="payment" :disabled="saving || verifying || needsConfirm">
            <option value="" disabled>Elegí un medio de pago</option>
            <option v-for="(label, method) in paymentLabels" :key="method" :value="method">{{ label }}</option>
          </select>
        </label>
        <button v-if="!needsConfirm" class="primary" type="button" :disabled="saving || verifying || !basketLines.length || !payment" @click="needsConfirm = true">Registrar venta</button>
        <div v-else class="gomar-feature-actions">
          <span v-if="payment">Medio de pago elegido: {{ paymentLabels[payment] }}.</span>
          <button class="primary" type="button" :disabled="saving || verifying" @click="complete">Confirmar y guardar venta</button>
          <button type="button" :disabled="saving" @click="needsConfirm = false">Volver sin guardar</button>
        </div>
        <small>Cobrá por fuera de Gomar. Este registro no verifica ni procesa pagos ni emite un comprobante fiscal.</small>
      </section>
      <section class="card gomar-feature">
        <div class="section-heading"><h2>Ventas de esta caja</h2>
          <button type="button" :disabled="!online || syncing || verifying || !pending" @click="sync">Sincronizar pendientes</button>
        </div>
        <p v-if="syncing" role="status">Sincronizando…</p>
        <ul v-if="recorded.length" class="gomar-feature-list">
          <li v-for="sale in [...recorded].sort((a, b) => b.created - a.created)" :key="sale.id">
            {{ localDateTime(new Date(sale.created).toISOString()) }} · {{ fmt(sale.total) }} · {{ paymentLabels[sale.paymentMethod ?? 'efectivo'] }} · {{ statusLabel[sale.status] }}
          </li>
        </ul>
        <p v-else>No hay ventas guardadas para este local.</p>
        <small>Si una venta necesita revisión, no la vuelvas a cargar.</small>
      </section>
    </template>
  </main>
</template>
