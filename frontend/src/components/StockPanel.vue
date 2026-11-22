<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { ApiError, request } from '../api'

interface ProductStock {
  producto_id: number
  nombre: string
  activo: boolean
  minimo: number
  saldo: number
  aviso?: 'negativo' | 'bajo'
}

interface Stock {
  activo: boolean
  productos: ProductStock[]
}

const props = defineProps<{ entity: number; outlet: number }>()
const current = ref<Stock | null>(null)
const pending = ref(false)
const error = ref('')
let task: AbortController | null = null
const format = new Intl.NumberFormat('es-AR')

async function readStock() {
  task?.abort()
  const next = new AbortController()
  task = next
  current.value = null
  error.value = ''
  pending.value = true
  try {
    const view = await request<Stock>(`/entities/${props.entity}/outlets/${props.outlet}/pos/stock`, {
      cache: 'no-store', signal: next.signal,
    })
    if (task !== next || next.signal.aborted) return
    if (typeof view.activo !== 'boolean' || !Array.isArray(view.productos)) throw new Error('Invalid stock view.')
    current.value = view
  } catch (err) {
    if (task !== next || next.signal.aborted) return
    error.value = err instanceof ApiError && err.status === 404
      ? 'El stock no está habilitado para este local.'
      : 'No se pudo cargar el stock. Volvé a intentar.'
  } finally {
    if (task === next) {
      task = null
      pending.value = false
    }
  }
}

watch(() => [props.entity, props.outlet], readStock, { immediate: true })
onBeforeUnmount(() => {
  task?.abort()
  task = null
})
</script>

<template>
  <section class="card gomar-feature" aria-label="Stock del local">
    <div class="section-heading">
      <div><p class="eyebrow">MOSTRADOR</p><h2>Stock del local</h2></div>
      <button type="button" :disabled="pending" @click="readStock">{{ error ? 'Reintentar' : 'Actualizar stock' }}</button>
    </div>
    <p class="muted">El saldo es estimado. Puede no incluir ventas de caja que todavía no se sincronizaron.</p>
    <p v-if="pending" role="status">Cargando stock…</p>
    <p v-else-if="error" role="alert">{{ error }}</p>
    <p v-else-if="current && !current.activo">El seguimiento de stock está desactivado para este local.</p>
    <p v-else-if="current && !current.productos.length">Todavía no hay productos con seguimiento de stock.</p>
    <ul v-else-if="current" class="gomar-feature-list">
      <li v-for="item in current.productos" :key="item.producto_id" class="gomar-feature-article">
        <div class="section-heading">
          <h3>{{ item.nombre }}</h3>
          <span v-if="!item.activo">Inactivo</span>
        </div>
        <p>Saldo estimado: {{ format.format(item.saldo) }} · Mínimo: {{ format.format(item.minimo) }}</p>
        <p v-if="item.aviso === 'negativo'" class="error">Stock negativo. Revisá el saldo.</p>
        <p v-else-if="item.aviso === 'bajo'" class="error">Stock por debajo del mínimo.</p>
      </li>
    </ul>
  </section>
</template>
