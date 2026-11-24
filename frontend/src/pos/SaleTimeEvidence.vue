<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { request } from '../api'
import SaleClock from './SaleClock.vue'
import { lookupClock, type ReceiptClock, type SaleTimeTarget } from './sale-time'

const props = defineProps<{ target: SaleTimeTarget }>()
const online = ref(navigator.onLine)
const evidence = ref<ReceiptClock>()
const error = ref('')
const busy = ref(false)
let controller: AbortController | undefined

function connection() { online.value = navigator.onLine }

function clear() {
  controller?.abort()
  controller = undefined
  evidence.value = undefined
  error.value = ''
  busy.value = false
}

watch([() => props.target, online], clear, { flush: 'sync' })
onMounted(() => {
  window.addEventListener('online', connection)
  window.addEventListener('offline', connection)
})
onUnmounted(() => {
  window.removeEventListener('online', connection)
  window.removeEventListener('offline', connection)
  clear()
})

async function lookup() {
  const desired = props.target
  if (!online.value || busy.value) return
  clear()
  busy.value = true
  const run = new AbortController()
  controller = run
  try {
    const raw = await request<unknown>(`/entities/${desired.entity}/outlets/${desired.outlet}/pos/sale-times/${desired.id}`, {
      cache: 'no-store', signal: AbortSignal.any([run.signal, AbortSignal.timeout(4000)]),
    })
    if (controller !== run) return
    evidence.value = lookupClock(raw, desired)
    if (!evidence.value) throw new Error('Respuesta no válida.')
  } catch {
    if (controller === run) error.value = 'No se pudo consultar la referencia con esta cuenta y local.'
  } finally {
    if (controller === run) busy.value = false
  }
}
</script>

<template>
  <main class="workspace pos-workspace">
    <section class="card gomar-feature" aria-labelledby="pos-sale-time-heading">
      <h1 id="pos-sale-time-heading">Horarios de la venta de Caja</h1>
      <p>Conectate e ingresá con la misma cuenta que registró la venta.</p>
      <button type="button" :disabled="!online || busy" @click="lookup">Consultar horarios</button>
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-if="evidence"><SaleClock :clock="evidence" /></p>
      <p>El reporte mensual usa el registro de Gomar, en hora de Buenos Aires. El reloj de la caja no acredita cuándo ocurrió la venta, un pago ni un comprobante fiscal.</p>
      <div class="gomar-feature-actions">
        <a href="/pos">Volver a Caja</a>
        <a href="/">Ingresá al espacio de trabajo</a>
      </div>
    </section>
  </main>
</template>
