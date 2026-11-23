<script setup lang="ts">
import { ref, watch } from 'vue'
import { ApiError, request } from '../api'
import { printedReferenceLabel } from '../document-reference'
import type { PrintedReference } from '../document-reference'
import { parsePayableEligibilityHandoff, payableEligibilityReference } from '../payable-eligibility-handoff'
import type { PayableEligibilityTarget } from '../payable-eligibility-handoff'

const props = defineProps<{ hash: string; search: string }>()
const state = ref<'loading' | 'signin' | 'unavailable' | 'eligible'>('loading')
const view = ref<{ target: PayableEligibilityTarget; reference: PrintedReference } | null>(null)

watch(() => [props.hash, props.search] as const, async ([hash, search], _, cleanup) => {
  const target = parsePayableEligibilityHandoff(hash, search)
  view.value = null
  if (!target) {
    state.value = 'unavailable'
    return
  }
  const controller = new AbortController()
  cleanup(() => controller.abort())
  state.value = 'loading'
  try {
    const response = await request<unknown>(
      `/entities/${target.entity}/outlets/${target.outlet}/documents/${target.document}/payable-eligibility-handoff`,
      { signal: controller.signal, cache: 'no-store', referrerPolicy: 'no-referrer', redirect: 'error' },
    )
    if (controller.signal.aborted) return
    const reference = payableEligibilityReference(response, target)
    if (!reference) {
      state.value = 'unavailable'
      return
    }
    view.value = { target, reference }
    state.value = 'eligible'
  } catch (cause) {
    if (controller.signal.aborted) return
    state.value = cause instanceof ApiError && cause.status === 401 ? 'signin' : 'unavailable'
  }
}, { immediate: true })
</script>

<template>
  <main class="login-page">
    <section class="login-card card" aria-labelledby="payable-eligibility-handoff-heading">
      <h1 id="payable-eligibility-handoff-heading">Revisar comprobante para cuentas a pagar</h1>
      <p v-if="state === 'loading'" role="status">Verificando el acceso y el comprobante…</p>
      <p v-else-if="state === 'signin'" role="status">Ingresá a Gomar con tu cuenta y volvé a abrir este enlace.</p>
      <p v-else-if="state === 'unavailable'" role="alert">No se pudo abrir un comprobante elegible en ese local.</p>
      <template v-else-if="view">
        <p><strong>{{ printedReferenceLabel(view.reference) }}</strong></p>
        <p>Comprobante {{ view.target.document }} · Local {{ view.target.outlet }}</p>
        <p>Actualmente cumple las condiciones del comprobante. Este enlace no registra ninguna cuenta ni ningún pago.</p>
        <p>En Gomar, elegí ese local y abrí <strong>Comprobantes → Buscar por referencia impresa</strong>. Ingresá el tipo y la letra (o «Sin letra»), el punto de venta y el número que figuran arriba. Si hay varias coincidencias, elegí el comprobante con el ID indicado.</p>
        <p>Si decidís registrarlo, el flujo habitual te va a pedir un motivo y volverá a verificar tus permisos y el comprobante.</p>
      </template>
      <a href="/" rel="noreferrer">Ir a Gomar</a>
    </section>
  </main>
</template>
