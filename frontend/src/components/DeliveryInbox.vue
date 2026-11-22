<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { ApiError, request } from '../api'
import { localDateTime } from '../format'

interface OrderItem {
  nombre: string
  cantidad: number
}

interface Order {
  id: string | number
  proveedor: string
  pedido_externo: string
  estado: 'aceptado' | 'recibido' | 'cancelado'
  items: OrderItem[]
  recibido_en: string
  actualizado_en: string
}

const props = defineProps<{ entity: number; outlet: number }>()
const orders = ref<Order[]>([])
const loading = ref(false)
const message = ref('')
let activeRequest: AbortController | null = null

function providerName(provider: string) {
  if (provider === 'pedidosya') return 'PedidosYa'
  if (provider === 'rappi') return 'Rappi'
  if (provider === 'uber_eats') return 'Uber Eats'
  return 'Delivery'
}

function statusName(status: Order['estado']) {
  if (status === 'aceptado') return 'Aceptado'
  if (status === 'recibido') return 'Recibido'
  if (status === 'cancelado') return 'Cancelado'
  return 'Estado sin informar'
}

function date(value: string) {
  return Number.isFinite(Date.parse(value)) ? localDateTime(value) : 'Sin informar'
}

async function load() {
  activeRequest?.abort()
  const task = new AbortController()
  activeRequest = task
  orders.value = []
  message.value = ''
  loading.value = true
  try {
    const path = `/entities/${props.entity}/outlets/${props.outlet}/delivery/orders`
    const result = await request<{ pedidos: Order[] }>(path, { cache: 'no-store', signal: task.signal })
    if (activeRequest !== task || task.signal.aborted) return
    if (!Array.isArray(result.pedidos)) throw new Error('Invalid delivery inbox.')
    orders.value = result.pedidos
  } catch (err) {
    if (activeRequest !== task || task.signal.aborted) return
    message.value = err instanceof ApiError && err.status === 404
      ? 'La bandeja de delivery no está habilitada para este local.'
      : 'No se pudieron cargar los pedidos. Volvé a intentar.'
  } finally {
    if (activeRequest === task) {
      activeRequest = null
      loading.value = false
    }
  }
}

watch(() => [props.entity, props.outlet], load, { immediate: true })
onBeforeUnmount(() => {
  activeRequest?.abort()
  activeRequest = null
})
</script>

<template>
  <section class="card gomar-feature" aria-label="Bandeja de delivery">
    <div class="section-heading">
      <div><p class="eyebrow">MOSTRADOR</p><h2>Bandeja de delivery</h2></div>
      <button type="button" :disabled="loading" @click="load">{{ message ? 'Reintentar' : 'Actualizar pedidos' }}</button>
    </div>
    <p class="muted">Consultá pedidos recibidos. Los cambios se hacen en la plataforma de delivery.</p>
    <p v-if="loading" role="status">Cargando pedidos…</p>
    <p v-else-if="message" role="alert">{{ message }}</p>
    <p v-else-if="!orders.length">Todavía no hay pedidos en este local.</p>
    <ul v-else class="gomar-feature-list">
      <li v-for="order in orders" :key="order.id" class="gomar-feature-article">
        <div class="section-heading">
          <h3>{{ providerName(order.proveedor) }} · {{ order.pedido_externo }}</h3>
          <strong>{{ statusName(order.estado) }}</strong>
        </div>
        <p class="muted">Recibido: {{ date(order.recibido_en) }} · Actualizado: {{ date(order.actualizado_en) }}</p>
        <ul v-if="order.items.length">
          <li v-for="(item, index) in order.items" :key="index">
            {{ item.cantidad }} × {{ item.nombre }}
          </li>
        </ul>
        <p v-else>No se informaron productos.</p>
      </li>
    </ul>
  </section>
</template>
