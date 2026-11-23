<script setup lang="ts">
import { ref, watch } from 'vue'
import { ApiError, request } from '../api'
import { parseReconciliationCloseHandoff } from '../reconciliation-close-handoff'

const props = defineProps<{ hash: string; search: string }>()
const state = ref<'loading' | 'signin' | 'unavailable' | 'eligible' | 'unsupported'>('loading')
const run = ref<number | null>(null)

watch(() => [props.hash, props.search] as const, async ([hash, search], _, cleanup) => {
  const target = parseReconciliationCloseHandoff(hash, search)
  run.value = null
  if (!target) {
    state.value = 'unavailable'
    return
  }
  const controller = new AbortController()
  cleanup(() => controller.abort())
  state.value = 'loading'
  try {
    const view = await request<{ run_id: number; state: string; eligible: boolean; blocker?: string }>(
      `/entities/${target.entity}/outlets/${target.outlet}/reconciliations/${target.run}/close-handoff`,
      { signal: controller.signal, cache: 'no-store', referrerPolicy: 'no-referrer' },
    )
    if (controller.signal.aborted) return
    if (view.run_id !== target.run || view.state !== 'review' ||
        !(view.eligible === true && view.blocker === undefined ||
          view.eligible === false && view.blocker === 'unsupported_review_data')) {
      state.value = 'unavailable'
      return
    }
    run.value = view.run_id
    state.value = view.eligible ? 'eligible' : 'unsupported'
  } catch (cause) {
    if (controller.signal.aborted) return
    state.value = cause instanceof ApiError && cause.status === 401 ? 'signin' : 'unavailable'
  }
}, { immediate: true })
</script>

<template>
  <main class="login-page">
    <section class="login-card card" aria-labelledby="reconciliation-close-handoff-heading">
      <h1 id="reconciliation-close-handoff-heading">Revisión de cierre de conciliación</h1>
      <p v-if="state === 'loading'" role="status">Verificando el acceso y las condiciones de cierre…</p>
      <p v-else-if="state === 'signin'" role="status">Ingresá a Gomar con tu cuenta y volvé a abrir este enlace.</p>
      <p v-else-if="state === 'unavailable'" role="alert">No se pudo abrir una conciliación en revisión en ese local.</p>
      <template v-else>
        <p>Conciliación #{{ run }} · En revisión</p>
        <p v-if="state === 'eligible'">En este momento cumple las condiciones de revisión para cerrarse.</p>
        <p v-else>El cierre no está disponible para estos datos de revisión.</p>
        <p>Este enlace no cierra ni aprueba nada. Para revisar los datos, entrá a Gomar y buscá esta conciliación en Conciliación financiera. Gomar volverá a verificar el estado y tus permisos si decidís cerrar.</p>
      </template>
      <a href="/" rel="noreferrer">Ir a Gomar</a>
    </section>
  </main>
</template>
