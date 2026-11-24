<script setup lang="ts">
import { computed } from 'vue'
import PosApp from './PosApp.vue'
import SaleTimeEvidence from './SaleTimeEvidence.vue'
import { saleTimeRoute } from './sale-time'

const props = defineProps<{ hash: string; search: string; emptyQuery: boolean }>()
const handoff = computed(() => saleTimeRoute(props.hash, props.search, props.emptyQuery))
</script>

<template>
  <SaleTimeEvidence v-if="handoff?.target" :target="handoff.target" />
  <main v-else-if="handoff" class="workspace pos-workspace">
    <section class="card gomar-feature" aria-labelledby="pos-invalid-sale-time-heading">
      <h1 id="pos-invalid-sale-time-heading">Enlace de horarios no válido</h1>
      <p>Volvé a abrir el enlace original de la venta.</p>
      <a href="/pos">Volver a Caja</a>
    </section>
  </main>
  <PosApp v-else />
</template>
