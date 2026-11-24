<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, request } from '../api'
import ThemeSelect from '../components/ThemeSelect.vue'

const props = defineProps<{ validAddress: boolean }>()
type Display = { organization: string; branches: string[] }
type Review = Display & { challenge: string }
type Recorded = Display & { state: 'confirmed' | 'prepared' }
type Status = { state: 'review' } | Recorded
type Phase = 'checking' | 'statusUnavailable' | 'suspended' | 'idle' | 'loading' | 'review' | 'saving' | 'complete' | 'unavailable' | 'uncertain'

const phase = ref<Phase>(props.validAddress ? 'checking' : 'idle')
const review = ref<Review>()
const recorded = ref<Recorded>()
const step = ref<HTMLElement>()
let pending: AbortController | null = null

function hasDisplay(value: unknown): value is Display {
  if (!value || typeof value !== 'object') return false
  const data = value as Partial<Display>
  const label = (v: unknown): v is string => typeof v === 'string' && v.trim().length > 0 && v.length <= 320
  return label(data.organization) && Array.isArray(data.branches) && data.branches.length >= 1
    && data.branches.length <= 64 && data.branches.every(label)
}

function isReview(value: unknown): value is Review {
  return hasDisplay(value) && 'challenge' in value && typeof value.challenge === 'string' && /^[A-Za-z0-9_-]{43}$/.test(value.challenge)
}

function isStatus(value: unknown): value is Status {
  if (!value || typeof value !== 'object' || !('state' in value)) return false
  if (value.state === 'review') return Object.keys(value).length === 1
  return (value.state === 'confirmed' || value.state === 'prepared') && hasDisplay(value)
}

function clear() {
  pending?.abort()
  pending = null
  review.value = undefined
  recorded.value = undefined
}

function suspendStatus() {
  clear()
  phase.value = 'suspended'
}

function reconsider() {
  const current = phase.value
  if (current === 'saving') {
    clear()
    phase.value = 'uncertain'
  } else if (current === 'checking' || current === 'complete') {
    suspendStatus()
  } else if (current === 'review' || current === 'loading') {
    clear()
    phase.value = 'idle'
  }
}

function pageHidden() {
  return document.visibilityState === 'hidden'
}

function resumeStatus() {
  if (phase.value === 'suspended' && !pageHidden()) void loadStatus()
}

function onVisibility() {
  if (pageHidden()) reconsider()
  else resumeStatus()
}

onMounted(() => {
  window.addEventListener('pagehide', reconsider)
  window.addEventListener('pageshow', resumeStatus)
  document.addEventListener('visibilitychange', onVisibility)
  if (props.validAddress) void loadStatus()
})
onUnmounted(() => {
  clear()
  window.removeEventListener('pagehide', reconsider)
  window.removeEventListener('pageshow', resumeStatus)
  document.removeEventListener('visibilitychange', onVisibility)
})

async function focusStep() {
  await nextTick()
  step.value?.focus()
}

async function loadStatus() {
  if (!props.validAddress || pending) return
  // A tab can mount hidden without seeing a visibilitychange event.
  if (pageHidden()) {
    suspendStatus()
    return
  }
  clear()
  phase.value = 'checking'
  const run = new AbortController()
  pending = run
  try {
    const result = await request<unknown>('/alta/estado', {
      method: 'POST', body: '{}', cache: 'no-store', signal: AbortSignal.any([run.signal, AbortSignal.timeout(8000)]),
    })
    if (pending !== run) return
    if (pageHidden()) {
      suspendStatus()
      return
    }
    if (!isStatus(result)) throw new Error('Respuesta no válida.')
    if (result.state === 'review') {
      phase.value = 'idle'
    } else {
      recorded.value = result
      phase.value = 'complete'
    }
  } catch {
    if (pending !== run) return
    if (pageHidden()) suspendStatus()
    else phase.value = 'statusUnavailable'
  } finally {
    if (pending === run) pending = null
  }
}

async function loadReview() {
  if (!props.validAddress || (phase.value !== 'idle' && phase.value !== 'unavailable')) return
  clear()
  phase.value = 'loading'
  const run = new AbortController()
  pending = run
  try {
    const result = await request<unknown>('/alta/revisar', {
      method: 'POST', body: '{}', cache: 'no-store', signal: AbortSignal.any([run.signal, AbortSignal.timeout(8000)]),
    })
    if (pending !== run) return
    if (!isReview(result)) throw new Error('Respuesta no válida.')
    review.value = result
    phase.value = 'review'
    void focusStep()
  } catch {
    if (pending === run) phase.value = 'unavailable'
  } finally {
    if (pending === run) pending = null
  }
}

async function confirm() {
  const original = review.value
  if (!props.validAddress || phase.value !== 'review' || !original) return
  clear()
  phase.value = 'saving'
  const run = new AbortController()
  pending = run
  try {
    await request<void>('/alta/confirmar', {
      method: 'POST', body: JSON.stringify({ challenge: original.challenge, confirm: true }),
      cache: 'no-store', signal: AbortSignal.any([run.signal, AbortSignal.timeout(8000)]),
    })
    if (pending !== run) return
    recorded.value = { state: 'confirmed', organization: original.organization, branches: original.branches }
    phase.value = 'complete'
    void focusStep()
  } catch (error) {
    if (pending === run) phase.value = error instanceof ApiError && error.status < 500 ? 'unavailable' : 'uncertain'
  } finally {
    if (pending === run) pending = null
  }
}

function cancelReview() {
  if (phase.value !== 'review') return
  clear()
  phase.value = 'idle'
  void focusStep()
}
</script>

<template>
  <main class="workspace native-signup">
    <header class="section-heading">
      <div><p class="eyebrow">GOMAR</p><h1>Revisá el alta</h1></div>
      <ThemeSelect />
    </header>
    <section class="card gomar-feature" aria-labelledby="native-signup-heading" :aria-busy="phase === 'checking' || phase === 'loading' || phase === 'saving'">
      <template v-if="!validAddress">
        <h2 id="native-signup-heading">Solicitud no disponible</h2>
        <p>Volvé a abrir el alta desde Gomar con la cuenta indicada.</p>
      </template>
      <template v-else-if="phase === 'review' && review">
        <h2 id="native-signup-heading" ref="step" class="reviewed-label" tabindex="-1">{{ review.organization }}</h2>
        <div>
          <h3>Locales incluidos</h3>
          <ul class="gomar-feature-list" aria-label="Locales incluidos">
            <li v-for="branch in review.branches" :key="branch" class="reviewed-label">{{ branch }}</li>
          </ul>
        </div>
        <p>Confirmá solo si la organización y los locales son los que esperabas. Esto no confirma un pago ni habilita el acceso.</p>
        <div class="gomar-feature-actions">
          <button class="primary" type="button" @click="confirm">Confirmar solicitud</button>
          <button type="button" @click="cancelReview">Volver sin confirmar</button>
        </div>
      </template>
      <template v-else-if="phase === 'complete' && recorded">
        <h2 id="native-signup-heading" ref="step" tabindex="-1">{{ recorded.state === 'prepared' ? 'Preparación registrada' : 'Confirmación recibida' }}</h2>
        <p class="reviewed-label"><strong>{{ recorded.organization }}</strong></p>
        <div>
          <h3>Locales incluidos</h3>
          <ul class="gomar-feature-list" aria-label="Locales incluidos">
            <li v-for="branch in recorded.branches" :key="branch" class="reviewed-label">{{ branch }}</li>
          </ul>
        </div>
        <p role="status">La puesta en marcha sigue pendiente. Este registro no acredita un pago ni activa el acceso.</p>
      </template>
      <template v-else-if="phase === 'statusUnavailable'">
        <h2 id="native-signup-heading">Solicitud no disponible</h2>
        <p role="alert">No pudimos consultar el estado. Revisá que sigas en la cuenta indicada.</p>
        <div class="gomar-feature-actions">
          <button type="button" @click="loadStatus">Volver a consultar</button>
        </div>
      </template>
      <template v-else-if="phase === 'checking' || phase === 'suspended'">
        <h2 id="native-signup-heading">Consultando el estado</h2>
      </template>
      <template v-else-if="phase === 'uncertain'">
        <h2 id="native-signup-heading">No pudimos verificar el resultado</h2>
        <p role="alert">Tu confirmación pudo haberse guardado. No la vuelvas a enviar sin verificar primero.</p>
      </template>
      <template v-else-if="phase === 'loading' || phase === 'saving'">
        <h2 id="native-signup-heading">{{ phase === 'saving' ? 'Guardando confirmación' : 'Cargando solicitud' }}</h2>
        <p role="status">{{ phase === 'saving' ? 'Esperá sin volver a enviar.' : 'Estamos verificando los datos.' }}</p>
      </template>
      <template v-else>
        <h2 id="native-signup-heading" ref="step" tabindex="-1">Confirmá tu solicitud</h2>
        <p v-if="phase === 'unavailable'" role="alert">La revisión no está disponible. Revisá que sigas en la cuenta indicada y volvé a intentar.</p>
        <p v-else>Revisá la organización y los locales antes de confirmar.</p>
        <div class="gomar-feature-actions">
          <button class="primary" type="button" @click="loadReview">Ver solicitud</button>
        </div>
      </template>
      <a v-if="phase !== 'saving'" href="/">Volver a Gomar</a>
    </section>
  </main>
</template>

<style scoped>
.native-signup { max-inline-size: 38rem; }
.native-signup > * + * { margin-block-start: 1rem; }
.native-signup > .card { padding: .875rem; }
.reviewed-label { white-space: break-spaces; overflow-wrap: anywhere; }
.native-signup button { min-block-size: 44px; }
</style>
