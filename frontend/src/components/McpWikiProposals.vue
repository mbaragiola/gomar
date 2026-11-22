<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, request } from '../api'

type Status = 'pending' | 'approved' | 'rejected' | 'expired' | 'revoked'
type Proposal = { id: number; status: Status; expires_at: string; article_id?: number; title?: string; body?: string; digest?: string }
type Decision = 'approve' | 'reject'
const props = defineProps<{ entity: number; outlet: number; outletName: string }>()
const base = `/entities/${props.entity}/outlets/${props.outlet}/wiki-create-proposals`
const keyPath = '/auth/mcp-proposal-token'
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
let expiryTimer: ReturnType<typeof setTimeout> | undefined

const labels: Record<Status, string> = {
  pending: 'Pendiente', approved: 'Publicada', rejected: 'Rechazada', expired: 'Vencida', revoked: 'Revocada',
}
const displayExpiry = (time: number) => new Intl.DateTimeFormat('es-AR', {
  hour: '2-digit', minute: '2-digit', timeZoneName: 'short',
}).format(time)
const canDecide = computed(() => viewed.value?.status === 'pending' &&
  /^[a-f0-9]{64}$/.test(viewed.value.digest ?? ''))

function hideKey() {
  secret = ''
  revealed.value = ''
  available.value = false
}

function keyDetailsChanged(event: Event) {
  if (event.target instanceof HTMLDetailsElement && !event.target.open) hideKey()
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
  clearPreview()
  hideKey()
}

function active(run: number) { return alive && opened.value && run === generation }

async function list(run: number) {
  const got = await request<{ proposals: Proposal[] }>(base, { cache: 'no-store' })
  if (active(run)) items.value = got.proposals
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
      error.value = cause.status === 410 ? 'La propuesta venció o se revocó su clave.' : 'La propuesta cambió. Volvé a cargarla antes de decidir.'
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
      (typeof got.title !== 'string' || typeof got.body !== 'string' || !/^[a-f0-9]{64}$/.test(got.digest ?? '')))) {
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
  if (busy.value || !target || !canDecide.value || target.id !== viewed.value?.id || target.digest !== viewed.value.digest) return
  await perform('No pude resolver la propuesta.', async run => {
    const result = await request<Proposal>(`${base}/${target.id}/decision`, {
      method: 'POST', cache: 'no-store',
      body: JSON.stringify({ decision: target.action, digest: target.digest, confirm: true }),
    })
    if (!active(run)) return
    if (result.id !== target.id || result.status !== (target.action === 'approve' ? 'approved' : 'rejected')) {
      throw new Error('unexpected decision receipt')
    }
    clearPreview()
    items.value = items.value.map(item => item.id === result.id ? result : item)
    notice.value = result.status === 'approved' ? 'Página publicada en el manual.' : 'Propuesta rechazada. No se publicó ninguna página.'
  })
}

function revokeKey(id: number) {
  return request<void>(`${keyPath}/${id}`, { method: 'DELETE', cache: 'no-store' })
}

async function issue() {
  if (credential.value) return
  await perform('No pude crear la clave para propuestas.', async run => {
    const got = await request<{ id: number; token: string; expires_at: string }>(keyPath, {
      method: 'POST', cache: 'no-store', body: JSON.stringify({ entity_id: props.entity, outlet_id: props.outlet }),
    })
    if (!Number.isSafeInteger(got.id) || got.id < 1) throw new Error('unexpected credential id')
    const expires = Date.parse(got.expires_at)
    if (!active(run) || !Number.isFinite(expires) || expires <= Date.now() || !/^[A-Za-z0-9_-]{43}$/.test(got.token)) {
      void revokeKey(got.id).catch(() => {})
      throw new Error('discarded credential')
    }
    credential.value = { id: got.id, expires }
    available.value = true
    secret = got.token
    expiryTimer = setTimeout(() => {
      clearKey()
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

async function revoke() {
  const id = credential.value?.id
  if (!id) return
  await perform('No pude revocar la clave.', async run => {
    await revokeKey(id)
    if (alive && credential.value?.id === id) clearKey()
    if (!active(run)) return
    clearPreview()
    notice.value = 'Clave revocada. No se pueden publicar sus propuestas pendientes.'
    try { await list(run) } catch { /* Revocation still succeeded. */ }
  })
}

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
    <button type="button" class="secondary" :aria-expanded="opened" aria-controls="wiki-proposals" @click="toggle">
      {{ opened ? 'Cerrar propuestas' : 'Propuestas del manual' }}
    </button>
  </div>
  <section v-if="opened" id="wiki-proposals" class="card subsection gomar-feature mcp-connect" aria-labelledby="wiki-proposals-heading" :aria-busy="busy">
    <h2 id="wiki-proposals-heading">Propuestas del manual</h2>
    <p>Claude o Codex pueden proponer una página sólo para <strong>{{ outletName }}</strong>. Nada se publica sin tu aprobación en Gomar.</p>
    <details @toggle="keyDetailsChanged">
      <summary>Clave para proponer páginas</summary>
      <p>No es la clave de lectura. Vence en 15 minutos. Cerrar el panel u ocultar la clave no la revoca.</p>
      <p v-if="credential">Vence a las {{ displayExpiry(credential.expires) }}.</p>
      <div class="gomar-feature-actions">
        <button v-if="!credential" type="button" class="secondary" :disabled="busy" @click="issue">Crear clave para propuestas</button>
        <template v-if="available">
          <button type="button" class="secondary" :disabled="busy" @click="copyKey">Copiar clave para propuestas</button>
          <button type="button" class="secondary" :disabled="busy" @click="showKey">Mostrar clave una vez</button>
        </template>
        <button v-if="revealed" type="button" class="secondary" @click="hideKey">Ocultar clave</button>
        <button v-if="credential" type="button" class="danger" :disabled="busy" @click="revoke">Revocar clave para propuestas</button>
      </div>
      <p v-if="revealed" class="mcp-connect-secret"><code>{{ revealed }}</code></p>
      <p>Guardá la clave en un archivo privado de tu equipo (permiso 0600). No la pegues en chats, URLs ni comandos.</p>
      <p>Configurá <code>gomar-mcp</code> con el archivo de lectura habitual y <code>--wiki-proposal-token-file</code> apuntando a este otro archivo privado.</p>
    </details>
    <div class="gomar-feature-actions">
      <button type="button" class="secondary" :disabled="busy" @click="refresh">Actualizar propuestas</button>
    </div>
    <p v-if="!items.length && !busy && !error">No hay propuestas para este local.</p>
    <ul v-else-if="items.length" class="gomar-feature-list">
      <li v-for="item in items" :key="item.id" class="gomar-feature-article">
        <span>{{ item.title || 'Propuesta ' + item.id }} · {{ labels[item.status] }}</span>
        <button type="button" class="text-button" :disabled="busy" @click="preview(item.id)">Ver propuesta {{ item.id }}</button>
      </li>
    </ul>
    <section v-if="viewed" class="card subsection gomar-feature" aria-label="Vista previa de la propuesta">
      <h3>Vista previa</h3>
      <template v-if="viewed.status === 'pending'">
        <p>Título: <strong>{{ viewed.title }}</strong></p>
        <pre class="gomar-feature-text proposal-body">{{ viewed.body }}</pre>
        <p>Si aprobás, esta página se agrega al manual de {{ outletName }}.</p>
        <div v-if="!deciding" class="gomar-feature-actions">
          <button type="button" class="primary" :disabled="busy || !canDecide" @click="begin('approve')">Revisé el texto: continuar</button>
          <button type="button" class="secondary" :disabled="busy || !canDecide" @click="begin('reject')">Rechazar propuesta</button>
          <button type="button" class="text-button" :disabled="busy" @click="clearPreview">Cerrar vista previa</button>
        </div>
        <div v-else class="gomar-feature-actions" role="group" aria-label="Confirmar decisión">
          <p>{{ deciding.action === 'approve' ? '¿Querés publicar este texto?' : '¿Querés rechazar esta propuesta sin publicarla?' }}</p>
          <button type="button" :class="deciding.action === 'approve' ? 'primary' : 'danger'" :disabled="busy" @click="decide">
            {{ deciding.action === 'approve' ? 'Aprobar y publicar' : 'Sí, rechazar' }}
          </button>
          <button type="button" class="secondary" :disabled="busy" @click="deciding = null">Volver sin cambios</button>
        </div>
      </template>
      <template v-else>
        <p>{{ labels[viewed.status] }}</p>
        <button type="button" class="text-button" @click="clearPreview">Cerrar vista previa</button>
      </template>
    </section>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>
</template>
