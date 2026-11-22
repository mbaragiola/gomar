<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { request } from '../api'

const props = defineProps<{ entity: number; outlet: number; outletName: string }>()
const opened = ref(false)
const busy = ref(false)
const notice = ref('')
const error = ref('')
const link = ref<{ id: number; expires: number } | null>(null)
const available = ref(false)
const revealed = ref('')
let secret = ''
let alive = true
let attempt = 0
let expiryTimer: ReturnType<typeof setTimeout> | undefined

const expiry = computed(() => link.value
  ? new Intl.DateTimeFormat('es-AR', { hour: '2-digit', minute: '2-digit', timeZoneName: 'short' }).format(link.value.expires)
  : '')

function hideSecret() {
  secret = ''
  available.value = false
  revealed.value = ''
}

function forgetLink() {
  if (expiryTimer !== undefined) clearTimeout(expiryTimer)
  expiryTimer = undefined
  hideSecret()
  link.value = null
}

function toggle() {
  opened.value = !opened.value
  if (!opened.value) {
    attempt++
    hideSecret()
    error.value = ''
    notice.value = ''
  }
}

function invalidate(id: number) {
  return request<void>(`/auth/mcp-token/${id}`, { method: 'DELETE', cache: 'no-store' })
}

async function issue() {
  if (!opened.value || busy.value || link.value) return
  const thisAttempt = ++attempt
  busy.value = true
  notice.value = error.value = ''
  try {
    const result = await request<{ id: number; token: string; expires_at: string }>('/auth/mcp-token', {
      method: 'POST',
      cache: 'no-store',
      body: JSON.stringify({ entity_id: props.entity, outlet_id: props.outlet }),
    })
    if (!Number.isSafeInteger(result.id) || result.id < 1) throw new Error('unexpected link id')
    // Requests that finish after closing or navigation must not reveal their
    // result. The existing delete endpoint authorizes only the same actor.
    if (!alive || !opened.value || thisAttempt !== attempt) {
      void invalidate(result.id).catch(() => {})
      return
    }
    const expires = Date.parse(result.expires_at)
    if (!Number.isFinite(expires) || expires <= Date.now() || !/^[A-Za-z0-9_-]{43}$/.test(result.token)) {
      void invalidate(result.id).catch(() => {})
      throw new Error('unexpected link response')
    }
    secret = result.token
    available.value = true
    link.value = { id: result.id, expires }
    expiryTimer = setTimeout(() => {
      forgetLink()
      notice.value = 'El acceso venció. Si lo necesitás, podés crear otro.'
    }, Math.min(expires - Date.now(), 2_147_483_647))
  } catch {
    if (alive && opened.value && thisAttempt === attempt) error.value = 'No pude crear el acceso. Probá de nuevo.'
  } finally {
    if (alive) busy.value = false
  }
}

async function copy() {
  if (!secret || !link.value || busy.value || link.value.expires <= Date.now()) return
  const thisAttempt = attempt
  busy.value = true
  error.value = ''
  try {
    await navigator.clipboard.writeText(secret)
    if (alive && opened.value && thisAttempt === attempt) {
      hideSecret()
      notice.value = 'Clave copiada. Pegala en el archivo privado de tu equipo.'
    }
  } catch {
    if (alive && opened.value && thisAttempt === attempt) error.value = 'No pude copiarla. Podés mostrarla una vez y copiarla a mano.'
  } finally {
    if (alive) busy.value = false
  }
}

function reveal() {
  if (!secret || !link.value || busy.value || link.value.expires <= Date.now()) return
  revealed.value = secret
  secret = ''
  available.value = false
}

async function revoke() {
  if (!link.value || busy.value) return
  const id = link.value.id
  busy.value = true
  error.value = ''
  try {
    await invalidate(id)
    if (alive) {
      forgetLink()
      notice.value = 'Acceso revocado.'
    }
  } catch {
    if (alive && opened.value) error.value = 'No pude revocar el acceso. Probá de nuevo.'
  } finally {
    if (alive) busy.value = false
  }
}

function pageHidden() {
  attempt++
  hideSecret()
}
onMounted(() => window.addEventListener('pagehide', pageHidden))
onBeforeUnmount(() => {
  window.removeEventListener('pagehide', pageHidden)
  alive = false
  attempt++
  forgetLink()
})
</script>

<template>
  <div class="appearance-toolbar mcp-connect-launch">
    <button type="button" class="secondary" :aria-expanded="opened" aria-controls="mcp-connect" @click="toggle">
      {{ opened ? 'Cerrar conexión' : 'Conectar Claude o Codex' }}
    </button>
  </div>
  <section v-if="opened" id="mcp-connect" class="card subsection gomar-feature mcp-connect" aria-labelledby="mcp-connect-heading">
    <h2 id="mcp-connect-heading">Conectar Claude o Codex</h2>
    <p>Sólo lectura para <strong>{{ outletName }}</strong>. No habilita cambios ni acceso a otros locales.</p>
    <p v-if="link">Vence a las {{ expiry }}. La clave se puede copiar o mostrar una sola vez.</p>
    <p v-else>El acceso vence en 15 minutos.</p>

    <div class="gomar-feature-actions">
      <button v-if="!link" class="primary" type="button" :disabled="busy" @click="issue">Crear acceso</button>
      <template v-if="available">
        <button class="primary" type="button" :disabled="busy" @click="copy">Copiar clave</button>
        <button class="secondary" type="button" :disabled="busy" @click="reveal">Mostrar una vez</button>
      </template>
      <button v-if="revealed" class="secondary" type="button" @click="hideSecret">Ocultar clave</button>
      <button v-if="link" class="danger" type="button" :disabled="busy" @click="revoke">Revocar acceso</button>
    </div>
    <p v-if="revealed" class="mcp-connect-secret"><code>{{ revealed }}</code></p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <details>
      <summary>Cómo usar la clave</summary>
      <p>Guardala sola en un archivo privado de tu equipo (permiso 0600). No la pegues en chats ni en comandos.</p>
      <p>En tu cliente, configurá <code>gomar-mcp</code> con el origen de este Gomar y la ruta absoluta al archivo, usando <code>--origin</code> y <code>--token-file</code>. Cuando venza, generá otra clave y reemplazá el contenido del archivo.</p>
    </details>
  </section>
</template>
