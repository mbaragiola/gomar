<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, request } from '../api'
import ThemeSelect from '../components/ThemeSelect.vue'

const props = defineProps<{ validAddress: boolean }>()
type Review = { organization: string; branches: string[]; challenge: string }
type Phase = 'idle' | 'loading' | 'review' | 'saving' | 'complete' | 'unavailable' | 'uncertain'

const phase = ref<Phase>('idle')
const review = ref<Review>()
const step = ref<HTMLElement>()
let pending: AbortController | null = null

function isReview(value: unknown): value is Review {
  if (!value || typeof value !== 'object') return false
  const data = value as Partial<Review>
  const label = (v: unknown): v is string => typeof v === 'string' && v.trim().length > 0 && v.length <= 320
  return label(data.organization) && typeof data.challenge === 'string'
    && /^[A-Za-z0-9_-]{43}$/.test(data.challenge)
    && Array.isArray(data.branches) && data.branches.length >= 1 && data.branches.length <= 64
    && data.branches.every(label)
}

function clear() {
  pending?.abort()
  pending = null
  review.value = undefined
}

function reconsider() {
  const current = phase.value
  if (current === 'saving') {
    clear()
    phase.value = 'uncertain'
  } else if (current === 'review' || current === 'loading') {
    clear()
    phase.value = 'idle'
  }
}

function onVisibility() {
  if (document.visibilityState === 'hidden') reconsider()
}

onMounted(() => {
  window.addEventListener('pagehide', reconsider)
  document.addEventListener('visibilitychange', onVisibility)
})
onUnmounted(() => {
  clear()
  window.removeEventListener('pagehide', reconsider)
  document.removeEventListener('visibilitychange', onVisibility)
})

async function focusStep() {
  await nextTick()
  step.value?.focus()
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
    <section class="card gomar-feature" aria-labelledby="native-signup-heading" :aria-busy="phase === 'loading' || phase === 'saving'">
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
      <template v-else-if="phase === 'complete'">
        <h2 id="native-signup-heading" ref="step" tabindex="-1">Confirmación recibida</h2>
        <p role="status">La puesta en marcha sigue pendiente. Tu confirmación no acredita un pago ni activa el acceso.</p>
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
.native-signup h2, .native-signup li { overflow-wrap: anywhere; }
.reviewed-label { white-space: break-spaces; }
.native-signup button { min-block-size: 44px; }
</style>
