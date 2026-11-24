<script setup lang="ts">
import { localDateTime } from '../format'
import type { ReceiptClock } from './sale-time'

defineProps<{ clock: ReceiptClock }>()
</script>

<template>
  <span class="pos-sale-clock">
    <span>Registrada por Gomar: <time :datetime="clock.registeredAt">{{ localDateTime(clock.registeredAt) }}</time>.</span>
    <span v-if="clock.tillReportedAt">
      Hora informada por la caja (no verificada):
      <time :datetime="clock.tillReportedAt">{{ localDateTime(clock.tillReportedAt) }}</time>.
    </span>
    <span v-else>Sin hora informada por la caja en el registro de Gomar.</span>
    <strong v-if="clock.status === 'review'">Revisá el reloj de la caja o la demora de sincronización. No vuelvas a cargar la venta.</strong>
  </span>
</template>

<style scoped>
.pos-sale-clock > * { display: block; }
</style>
