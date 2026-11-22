<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ApiError, request } from '../api'

type Status = 'pending' | 'approved' | 'rejected' | 'expired' | 'revoked'
type Proposal = { id: number; status: Status; expires_at: string; product_id?: number; product_revision?: number; rename?: Rename; digest?: string }
type Rename = {
  product_id: number; expected_revision: number; name: string; current_name: string
  unchanged_price_minor: number; unchanged_active: boolean
}
type Session = { id: number; expires_at: string; actions: string[] }
const actionName = 'pos_product_rename'
const actionLabel = 'Cambiar el nombre de un producto de caja'
const positive = (value: number) => Number.isSafeInteger(value) && value > 0
function isRename(value: Rename | undefined): value is Rename {
  return !!value && positive(value.product_id) && positive(value.expected_revision) &&
    typeof value.name === 'string' && typeof value.current_name === 'string' &&
    Number.isSafeInteger(value.unchanged_price_minor) && value.unchanged_price_minor >= 0 &&
    typeof value.unchanged_active === 'boolean'
}
const prices = new Intl.NumberFormat('es-AR', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
type Decision = 'approve' | 'reject'
const props = defineProps<{ entity: number; outlet: number; outletName: string; canRename: boolean }>()
const base = `/entities/${props.entity}/outlets/${props.outlet}/pos-rename-proposals`
const keyPath = '/auth/mcp-proposal-sessions'
const sessionPath = `/entities/${props.entity}/outlets/${props.outlet}/proposal-sessions`
const selected = ref(false)
const sessions = ref<Session[]>([])
const items = ref<Proposal[]>([])
const viewed = ref<Proposal | null>(null)
const deciding = ref<{ id: number; digest: string; action: Decision } | null>(null)
const opened = ref(false)
const busy = ref(false)
const notice = ref('')
const error = ref('')
const credential = ref<{ id: number; expires: number } | null>(null)
const available = ref(false)
const revealed = ref('')
let secret = ''
let alive = true
let generation = 0
let disclosure = 0
let expiryTimer: ReturnType<typeof setTimeout> | undefined

const labels: Record<Status, string> = {
  pending: 'Pendiente', approved: 'Aplicada', rejected: 'Rechazada', expired: 'Vencida', revoked: 'Revocada',
}
const displayExpiry = (time: number) => new Intl.DateTimeFormat('es-AR', {
  hour: '2-digit', minute: '2-digit', timeZoneName: 'short',
}).format(time)
const canDecide = computed(() => viewed.value?.status === 'pending' && props.canRename && isRename(viewed.value.rename) &&
  /^[a-f0-9]{64}$/.test(viewed.value.digest ?? ''))

function hideKey() {
  disclosure++
  secret = ''
  revealed.value = ''
  available.value = false
}

function keyDetailsChanged(event: Event) {
  if (event.target instanceof HTMLDetailsElement && !event.target.open) {
    selected.value = false
    hideKey()
  }
}

function clearKey() {
  if (expiryTimer !== undefined) clearTimeout(expiryTimer)
  expiryTimer = undefined
  hideKey()
  credential.value = null
}

function clearPreview() {
  deciding.value = null
  viewed.value = null
}

function dismiss() {
  generation++
  opened.value = false
  busy.value = false
  error.value = notice.value = ''
  items.value = []
  sessions.value = []
  selected.value = false
  clearPreview()
  hideKey()
}

function active(run: number) { return alive && opened.value && run === generation }

async function list(run: number) {
  const got = await request<{ sessions: Session[] }>(sessionPath, { cache: 'no-store' })
  if (!Array.isArray(got.sessions) || !got.sessions.every(s => positive(s.id) &&
    Number.isFinite(Date.parse(s.expires_at)) && Array.isArray(s.actions) && s.actions.every(a => typeof a === 'string'))) {
    throw new Error('unexpected session list')
  }
  if (active(run)) sessions.value = got.sessions
  if (!props.canRename || !active(run)) return
  const pending = await request<{ proposals: Proposal[] }>(base, { cache: 'no-store' })
  if (active(run)) items.value = pending.proposals
}

async function refresh() {
  if (busy.value) return
  clearPreview()
  await perform('No pude cargar las propuestas.', list)
}

async function toggle() {
  if (opened.value) { dismiss(); return }
  opened.value = true
  await refresh()
}

async function perform(message: string, action: (run: number) => Promise<void>) {
  if (busy.value) return
  const run = generation
  busy.value = true
  error.value = notice.value = ''
  try {
    await action(run)
  } catch (cause) {
    if (!active(run)) return
    if (cause instanceof ApiError && (cause.status === 409 || cause.status === 410)) {
      clearPreview()
      error.value = cause.status === 410 ? 'La propuesta venció o se revocó su clave.' : 'No se cambió el nombre. La propuesta o la versión del producto cambiaron.'
      try { await list(run) } catch { /* Keep the decision error. */ }
    } else {
      error.value = message
    }
  } finally {
    if (active(run)) busy.value = false
  }
}

async function preview(id: number) {
  clearPreview()
  await perform('No pude abrir la propuesta.', async run => {
    const got = await request<Proposal>(`${base}/${id}`, { cache: 'no-store' })
    if (got.id !== id || (got.status === 'pending' &&
      (!isRename(got.rename) || !/^[a-f0-9]{64}$/.test(got.digest ?? '')))) {
      throw new Error('unexpected proposal response')
    }
    if (active(run)) viewed.value = got
  })
}

function begin(action: Decision) {
  if (!canDecide.value || busy.value || !viewed.value?.digest) return
  deciding.value = { id: viewed.value.id, digest: viewed.value.digest, action }
}

async function decide() {
  const target = deciding.value
  const rename = viewed.value?.rename
  if (busy.value || !target || !rename || !canDecide.value || target.id !== viewed.value?.id || target.digest !== viewed.value.digest) return
  await perform('No pude resolver la propuesta.', async run => {
    const result = await request<Proposal>(`${base}/${target.id}/decision`, {
      method: 'POST', cache: 'no-store',
      body: JSON.stringify({ decision: target.action, digest: target.digest, confirm: true }),
    })
    if (!active(run)) return
    if (result.id !== target.id || result.status !== (target.action === 'approve' ? 'approved' : 'rejected') ||
      (target.action === 'approve' && (result.product_id !== rename.product_id || result.product_revision !== rename.expected_revision + 1))) {
      throw new Error('unexpected decision receipt')
    }
    clearPreview()
    items.value = items.value.map(item => item.id === result.id ? result : item)
    notice.value = result.status === 'approved' ? 'Nombre del producto actualizado.' : 'Propuesta rechazada. No se modificó el producto.'
  })
}

function revokeKey(id: number) {
  return request<void>(`${keyPath}/${id}`, { method: 'DELETE', cache: 'no-store' })
}

async function issue() {
  if (credential.value || !props.canRename || !selected.value) return
  const disclosureRun = disclosure
  await perform('No pude crear la sesión de propuestas.', async run => {
    const got = await request<Session & { token: string }>(keyPath, {
      method: 'POST', cache: 'no-store', body: JSON.stringify({ entity_id: props.entity, outlet_id: props.outlet, actions: [actionName] }),
    })
    if (!Number.isSafeInteger(got.id) || got.id < 1) throw new Error('unexpected credential id')
    const expires = Date.parse(got.expires_at)
    if (!active(run) || !props.canRename || disclosureRun !== disclosure || !Number.isFinite(expires) || expires <= Date.now() || !Array.isArray(got.actions) ||
      got.actions.length !== 1 || got.actions[0] !== actionName || !/^[A-Za-z0-9_-]{43}$/.test(got.token)) {
      void revokeKey(got.id).catch(() => {})
      throw new Error('discarded credential')
    }
    credential.value = { id: got.id, expires }
    selected.value = false
    sessions.value = [...sessions.value, { id: got.id, expires_at: got.expires_at, actions: got.actions }]
    available.value = true
    secret = got.token
    expiryTimer = setTimeout(() => {
      clearKey()
      sessions.value = sessions.value.filter(session => session.id !== got.id)
      clearPreview()
      if (opened.value) notice.value = 'La clave venció.'
    }, Math.min(expires - Date.now(), 2_147_483_647))
  })
}

async function copyKey() {
  if (!secret || !credential.value || credential.value.expires <= Date.now()) return
  await perform('No pude copiar la clave. Podés mostrarla una vez y copiarla a mano.', async run => {
    await navigator.clipboard.writeText(secret)
    if (active(run)) {
      hideKey()
      notice.value = 'Clave copiada. Pegala en el archivo privado de tu equipo.'
    }
  })
}

function showKey() {
  if (!secret || !credential.value || credential.value.expires <= Date.now() || busy.value) return
  revealed.value = secret
  secret = ''
  available.value = false
}

async function revoke(id: number) {
  await perform('No pude revocar la clave.', async run => {
    await revokeKey(id)
    if (alive && credential.value?.id === id) clearKey()
    if (!active(run)) return
    clearPreview()
    sessions.value = sessions.value.filter(session => session.id !== id)
    notice.value = 'Sesión revocada. Sus propuestas pendientes no se pueden aprobar.'
    try { await list(run) } catch { /* Revocation still succeeded. */ }
  })
}

watch(() => props.canRename, allowed => {
  if (!allowed) {
    selected.value = false
    clearKey()
    clearPreview()
    items.value = []
  }
})
onMounted(() => window.addEventListener('pagehide', dismiss))
onBeforeUnmount(() => {
  alive = false
  window.removeEventListener('pagehide', dismiss)
  dismiss()
  clearKey()
})
</script>

<template>
  <div class="appearance-toolbar mcp-connect-launch">
    <button type="button" class="secondary" :aria-expanded="opened" aria-controls="pos-rename-proposals" @click="toggle">
      {{ opened ? 'Cerrar sesiones y propuestas' : 'Sesiones de propuestas' }}
    </button>
  </div>
  <section v-if="opened" id="pos-rename-proposals" class="card subsection gomar-feature mcp-connect" aria-labelledby="pos-rename-proposals-heading" :aria-busy="busy">
    <h2 id="pos-rename-proposals-heading">Sesiones de propuestas</h2>
    <p>Claude o Codex pueden preparar solo las acciones que elijas para <strong>{{ outletName }}</strong>. No aplica cambios. Vas a aprobarlos en Gomar.</p>
    <details v-if="canRename" @toggle="keyDetailsChanged">
      <summary>Qué puede proponer este chat</summary>
      <p>Esta sesión es distinta de la clave de lectura y vence en 15 minutos como máximo. Cerrar el panel u ocultar la clave no termina la sesión.</p>
      <p v-if="credential">Vence a las {{ displayExpiry(credential.expires) }}.</p>
      <label v-if="!credential" class="mcp-session-action">
        <input v-model="selected" type="checkbox" :disabled="busy" />
        {{ actionLabel }}
      </label>
      <div class="gomar-feature-actions">
        <button v-if="!credential" type="button" class="secondary" :disabled="busy || !selected" @click="issue">Crear sesión de propuestas</button>
        <template v-if="available">
          <button type="button" class="secondary" :disabled="busy" @click="copyKey">Copiar clave para propuestas</button>
          <button type="button" class="secondary" :disabled="busy" @click="showKey">Mostrar clave una vez</button>
        </template>
        <button v-if="revealed" type="button" class="secondary" @click="hideKey">Ocultar clave</button>
      </div>
      <p v-if="revealed" class="mcp-connect-secret"><code>{{ revealed }}</code></p>
      <p>Guardá la clave en un archivo privado de tu equipo (permiso 0600). No la pegues en chats, URLs ni comandos.</p>
      <p>Configurá <code>gomar-mcp</code> con el archivo de lectura habitual y <code>--proposal-session-token-file</code> apuntando a este otro archivo privado.</p>
    </details>
    <h3>Sesiones activas de {{ outletName }}</h3>
    <p v-if="!sessions.length && !busy && !error">No hay sesiones activas para este local.</p>
    <ul v-else-if="sessions.length" class="gomar-feature-list">
      <li v-for="session in sessions" :key="session.id" class="gomar-feature-article">
        <span>Sesión {{ session.id }} · {{ session.actions.map(action => action === actionName ? actionLabel : 'Acción no reconocida').join(', ') }} · Vence a las {{ displayExpiry(Date.parse(session.expires_at)) }}</span>
        <button type="button" class="danger" :disabled="busy" @click="revoke(session.id)">Revocar sesión {{ session.id }}</button>
      </li>
    </ul>
    <div class="gomar-feature-actions">
      <button type="button" class="secondary" :disabled="busy" @click="refresh">Actualizar sesiones y propuestas</button>
    </div>
    <p v-if="canRename && !items.length && !busy && !error">No hay propuestas para este local.</p>
    <ul v-else-if="canRename && items.length" class="gomar-feature-list">
      <li v-for="item in items" :key="item.id" class="gomar-feature-article">
        <span>{{ 'Propuesta ' + item.id }} · {{ labels[item.status] }}</span>
        <button type="button" class="text-button" :disabled="busy" @click="preview(item.id)">Ver propuesta {{ item.id }}</button>
      </li>
    </ul>
    <section v-if="canRename && viewed" class="card subsection gomar-feature" aria-label="Vista previa de la propuesta">
      <h3>Vista previa</h3>
      <template v-if="viewed.status === 'pending' && viewed.rename">
        <p>Local: <strong>{{ outletName }}</strong> · Producto {{ viewed.rename.product_id }} · Versión {{ viewed.rename.expected_revision }}</p>
        <p>Nombre actual:</p>
        <pre class="gomar-feature-text proposal-body" aria-label="Nombre actual">{{ viewed.rename.current_name }}</pre>
        <p>Nombre propuesto:</p>
        <pre class="gomar-feature-text proposal-body" aria-label="Nombre propuesto">{{ viewed.rename.name }}</pre>
        <p>Precio sin cambios: ARS {{ prices.format(viewed.rename.unchanged_price_minor / 100) }}</p>
        <p>Estado sin cambios: {{ viewed.rename.unchanged_active ? 'Activo' : 'Inactivo' }}.</p>
        <p>Solo se cambia el nombre. No se modifican el stock ni las ventas.</p>
        <div v-if="!deciding" class="gomar-feature-actions">
          <button type="button" class="primary" :disabled="busy || !canDecide" @click="begin('approve')">Continuar con este cambio</button>
          <button type="button" class="secondary" :disabled="busy || !canDecide" @click="begin('reject')">Rechazar propuesta</button>
          <button type="button" class="text-button" :disabled="busy" @click="clearPreview">Cerrar vista previa</button>
        </div>
        <div v-else class="gomar-feature-actions" role="group" aria-label="Confirmar decisión">
          <p>{{ deciding.action === 'approve' ? '¿Querés cambiar solo el nombre de este producto?' : '¿Querés rechazar la propuesta sin modificar el producto?' }}</p>
          <button type="button" :class="deciding.action === 'approve' ? 'primary' : 'danger'" :disabled="busy" @click="decide">
            {{ deciding.action === 'approve' ? 'Confirmar cambio de nombre' : 'Sí, rechazar' }}
          </button>
          <button type="button" class="secondary" :disabled="busy" @click="deciding = null">Volver sin cambios</button>
        </div>
      </template>
      <template v-else>
        <p>{{ labels[viewed.status] }}</p>
        <p v-if="viewed.status === 'approved' && viewed.product_id">Producto {{ viewed.product_id }} · Versión {{ viewed.product_revision }}</p>
        <button type="button" class="text-button" @click="clearPreview">Cerrar vista previa</button>
      </template>
    </section>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.mcp-session-action { display: flex; align-items: baseline; gap: .5rem; }
.mcp-session-action input { width: auto; }
</style>
