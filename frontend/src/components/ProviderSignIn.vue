<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { request } from '../api'

const props = defineProps<{ disabled: boolean }>()
const emit = defineEmits<{
  busy: [value: boolean]
  options: [value: { passwordEnabled: boolean; recoveryEnabled: boolean; employeeEnabled: boolean }]
}>()
const providers = ref<Provider[]>([])
const loading = ref(true)
const unavailable = ref(false)
const starting = ref<Provider | null>(null)
const error = ref('')
const lifetime = new AbortController()
let navigation = 0

const labels = {
  google: 'Google',
  microsoft: 'Microsoft',
  'microsoft-work': 'Microsoft (trabajo o estudio)',
} as const
type Provider = keyof typeof labels

function isProvider(value: unknown): value is Provider {
  return typeof value === 'string' && Object.hasOwn(labels, value)
}

function pinnedAuthorization(provider: Provider, raw: unknown) {
  if (typeof raw !== 'string') throw new Error('invalid authorization')
  const url = new URL(raw)
  const microsoft = 'https://login.microsoftonline.com'
  const valid = provider === 'google'
    ? url.origin === 'https://accounts.google.com' && url.pathname === '/o/oauth2/v2/auth'
    : url.origin === microsoft && (provider === 'microsoft'
        ? url.pathname === '/consumers/oauth2/v2.0/authorize'
        : /^\/[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}\/oauth2\/v2\.0\/authorize$/.test(url.pathname))
  if (!valid || url.username || url.password || url.hash) throw new Error('invalid authorization')
  return url.href
}

function cancelStart() {
  navigation++
  if (starting.value !== null) {
    starting.value = null
    emit('busy', false)
  }
}

onMounted(async () => {
  window.addEventListener('pagehide', cancelStart)
  try {
    const result = await request<{ providers: { id?: unknown }[]; local_password_enabled?: unknown; admin_recovery_enabled?: unknown; employee_password_enabled?: unknown }>('/auth/providers', {
      cache: 'no-store', signal: lifetime.signal,
    })
    if (lifetime.signal.aborted) return
    if (!Array.isArray(result.providers)) throw new Error('invalid provider list')
    providers.value = [...new Set(result.providers.map(p => p?.id).filter(isProvider))]
    const classicEnabled = result.local_password_enabled === true
    if (!classicEnabled && !(['google', 'microsoft'] as const).every(id => providers.value.includes(id))) {
      unavailable.value = true
    }
    emit('options', { passwordEnabled: classicEnabled, recoveryEnabled: result.admin_recovery_enabled === true, employeeEnabled: result.employee_password_enabled === true })
  } catch {
    if (!lifetime.signal.aborted) {
      unavailable.value = true
      emit('options', { passwordEnabled: false, recoveryEnabled: false, employeeEnabled: false })
    }
  } finally {
    if (!lifetime.signal.aborted) loading.value = false
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('pagehide', cancelStart)
  lifetime.abort()
  cancelStart()
})

async function start(provider: Provider) {
  if (props.disabled || loading.value || starting.value || !providers.value.includes(provider)) return
  const attempt = ++navigation
  starting.value = provider
  error.value = ''
  emit('busy', true)
  try {
    const result = await request<{ authorization_url?: unknown }>(`/auth/providers/${provider}/start`, {
      method: 'POST', cache: 'no-store', signal: lifetime.signal,
    })
    if (lifetime.signal.aborted) return
    if (attempt !== navigation) return
    window.location.assign(pinnedAuthorization(provider, result.authorization_url))
  } catch {
    if (!lifetime.signal.aborted && attempt === navigation) {
      error.value = 'No pude iniciar sesión con esa cuenta. Volvé a intentarlo.'
      starting.value = null
      emit('busy', false)
    }
  }
}
</script>

<template>
  <section v-if="loading || unavailable || providers.length" class="stack" aria-label="Ingreso con un proveedor">
    <p v-if="loading" class="muted" role="status">Consultando opciones de ingreso…</p>
    <p v-else-if="unavailable" class="muted" role="status">Las opciones de ingreso con proveedores no están disponibles.</p>
    <template v-else>
      <button
        v-for="provider in providers" :key="provider" type="button" class="secondary"
        :disabled="disabled || starting !== null" @click="start(provider)"
      >
        {{ starting === provider ? `Abriendo ${labels[provider]}…` : `Continuar con ${labels[provider]}` }}
      </button>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </template>
  </section>
</template>
