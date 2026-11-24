<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { localDateTime } from '../format'
import type { Grant } from './model'
import { precloseSummary } from './preclose'
import SaleClock from './SaleClock.vue'
import { saleTimeHandoff } from './sale-time'
import { localLease, readRegisterSales } from './storage'

const props = defineProps<{ scope: Grant; updating: boolean }>()
const summary = ref<ReturnType<typeof precloseSummary>>()
const checkedAt = ref('')
const loading = ref(false)
const failed = ref(false)
let generation = 0

function clear() {
  generation++
  loading.value = false
  failed.value = false
  summary.value = undefined
}

watch(() => props.updating, clear, { flush: 'sync' })
onUnmounted(clear)

async function inspect() {
  if (props.updating || loading.value) return
  clear()
  const token = generation
  const scope = props.scope
  const now = Date.now()
  loading.value = true
  try {
    if (localLease() !== scope.lease || now >= scope.expires) throw new Error()
    const sales = await readRegisterSales(scope, now)
    if (token !== generation) return
    if (!sales || localLease() !== scope.lease || Date.now() >= scope.expires) throw new Error()
    summary.value = precloseSummary(scope, sales, now)
    checkedAt.value = new Date(now).toISOString()
  } catch {
    if (token === generation) failed.value = true
  } finally {
    if (token === generation) loading.value = false
  }
}

const period = (value: string) => value.split('-').reverse().join('/')
</script>

<template>
  <section class="card gomar-feature" aria-labelledby="pos-preclose-heading">
    <h2 id="pos-preclose-heading">Antes del cierre</h2>
    <p><strong>Otras cajas sin verificar.</strong> Antes de contar el stock o cerrar el mes, revisá y sincronizá todas las cajas del local.</p>
    <p>Local: {{ scope.label }}. Solo las ventas guardadas en este navegador para la cuenta que habilitó la caja. No es un historial completo del local.</p>
    <button type="button" :disabled="updating || loading" @click="inspect">Consultar ventas de este navegador</button>
    <p v-if="loading" role="status">Leyendo las ventas guardadas…</p>
    <p v-if="failed" role="alert">No se pudieron leer las ventas de esta caja. Verificá que siga habilitada y volvé a consultar.</p>
    <template v-if="summary">
      <p>Consultado: <time :datetime="checkedAt">{{ localDateTime(checkedAt) }}</time> (Buenos Aires). Volvé a consultar después de sincronizar.</p>
      <h3>Pendientes o para revisar</h3>
      <p>Pendientes de sincronizar: {{ summary.pending }}. Necesitan revisión: {{ summary.attention }}.</p>
      <ul v-if="summary.outstanding.length" class="gomar-feature-list pos-preclose-refs" aria-label="Ventas pendientes o para revisar">
        <li v-for="sale in summary.outstanding" :key="sale.id">
          <strong>{{ sale.status === 'pending' ? 'Pendiente' : 'Necesita revisión' }}</strong>
          · Referencia: {{ sale.id }}
          <div v-if="sale.tillReportedAt">
            Hora informada por la caja (no verificada):
            <time :datetime="sale.tillReportedAt">{{ localDateTime(sale.tillReportedAt) }}</time>.
          </div>
          <div v-else>Hora informada por la caja: desconocida.</div>
          <div>Registro de Gomar: sin confirmar desde este navegador.</div>
        </li>
      </ul>
      <p v-if="summary.attention">Si una venta necesita revisión, no la vuelvas a cargar.</p>

      <h3>Ventas sincronizadas con horarios en meses distintos</h3>
      <p>Meses revisados según la fecha de registro de Gomar en Buenos Aires: {{ period(summary.previous) }} y {{ period(summary.current) }} (mes actual según este navegador).</p>
      <ul v-if="summary.crossMonth.length" class="gomar-feature-list pos-preclose-refs" aria-label="Ventas sincronizadas con horarios en meses distintos">
        <li v-for="sale in summary.crossMonth" :key="sale.id">
          Referencia:
          <a v-if="scope.tillTimeV1" :href="saleTimeHandoff({ ...scope, id: sale.id })" target="_blank" rel="noopener">{{ sale.id }}</a>
          <span v-else>{{ sale.id }}</span>
          <SaleClock :clock="sale.clock" />
        </li>
      </ul>
      <p v-else>No se detectaron diferencias de mes en las ventas con ambos horarios disponibles.</p>
      <p>Hora de caja desconocida en los meses revisados: {{ summary.unknownTill }}. Fecha de registro desconocida en este navegador: {{ summary.unknownRegistration }} (no se sabe de qué mes son).</p>
      <p>El reporte mensual sigue usando la fecha de registro de Gomar. Esta consulta no modifica ventas, reportes ni stock.</p>
    </template>
  </section>
</template>

<style scoped>
.pos-preclose-refs li { overflow-wrap: anywhere; }
</style>
