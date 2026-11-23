<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, request } from '../api'
import type { Context, Line } from './model'

const props = defineProps<{
  scope: Context
  cart: ReadonlyArray<Line>
  online: boolean
  allowed: () => boolean
  formatMoney: (amount: number) => string
}>()

interface Check {
  mesa: number
  total_centavos: number
  items: Line[]
}

const number = ref('')
const loaded = ref<Check | null>(null)
const error = ref('')
const busy = ref(false)
let controller: AbortController | undefined
let retry: { payload: string; id: string } | undefined
let active = true
const table = computed(() => /^[1-9][0-9]{0,3}$/.test(number.value) ? Number(number.value) : 0)

function linkTable() {
  const m = /^#entity=([1-9][0-9]*)&outlet=([1-9][0-9]*)&table=([1-9][0-9]{0,3})$/.exec(window.location.hash)
  if (!m || !props.allowed() || Number(m[1]) !== props.scope.entity || Number(m[2]) !== props.scope.outlet) return
  controller?.abort()
  number.value = m[3]
  error.value = ''
  loaded.value = null
}

function changeTable() {
  error.value = ''
  loaded.value = null
}

async function act(save: boolean) {
  if (!active || !props.allowed() || !props.online || !table.value || busy.value || save && !props.cart.length) return
  const picked = table.value
  const path = `/entities/${props.scope.entity}/outlets/${props.scope.outlet}/pos/table-checks`
  const data = { mesa: picked, items: props.cart.map(({ producto_id, revision, cantidad }) => ({ producto_id, revision, cantidad })) }
  const payload = JSON.stringify(data)
  const next = new AbortController()
  controller = next
  busy.value = true
  loaded.value = null
  error.value = ''
  try {
    if (save && retry?.payload !== payload) retry = { payload, id: crypto.randomUUID() }
    const response = await request<Check>(save ? path : path + '/' + picked, {
      ...(save ? { method: 'POST', body: JSON.stringify({ ...data, id_solicitud: retry?.id }) } : {}),
      cache: 'no-store', signal: AbortSignal.any([next.signal, AbortSignal.timeout(8000)]),
    })
    if (!active || next.signal.aborted || !props.allowed() || table.value !== picked) return
    if (response.mesa !== picked || !Number.isSafeInteger(response.total_centavos) || response.total_centavos < 1
      || !Array.isArray(response.items) || !response.items.length) throw new Error('Respuesta no válida')
    loaded.value = response
  } catch (err) {
    if (!active || next.signal.aborted || !props.allowed() || table.value !== picked) return
    error.value = err instanceof ApiError ? err.message : 'No se pudo verificar la cuenta. Consultá la mesa antes de volver a guardar.'
  } finally {
    if (controller === next) {
      controller = undefined
      busy.value = false
    }
  }
}

onMounted(() => {
  linkTable()
  window.addEventListener('hashchange', linkTable)
})
onUnmounted(() => {
  active = false
  controller?.abort()
  window.removeEventListener('hashchange', linkTable)
})
</script>

<template>
  <section class="card gomar-feature" aria-labelledby="saved-table-check-heading">
    <h2 id="saved-table-check-heading">Cuentas guardadas de mesa</h2>
    <p>Guardá los productos de esta pestaña para consultar la cuenta en otra caja del mismo local. No queda registrada como venta ni como pago.</p>
    <p>En esta vista todavía no se pueden editar, cerrar ni cobrar cuentas guardadas.</p>
    <p v-if="!online">Conectate para guardar o consultar cuentas.</p>
    <label>Número de mesa
      <input v-model="number" type="text" inputmode="numeric" pattern="[1-9][0-9]{0,3}" maxlength="4" :disabled="!online || busy" @input="changeTable" />
    </label>
    <div class="gomar-feature-actions">
      <button type="button" :disabled="!online || busy || !table" @click="act(false)">Consultar mesa</button>
      <button type="button" :disabled="!online || busy || !table || !cart.length" @click="act(true)">Guardar cuenta sin cobro</button>
    </div>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <div v-if="loaded && online" aria-live="polite">
      <h3>Cuenta guardada: mesa {{ loaded.mesa }}</h3>
      <ul aria-label="Productos de la cuenta guardada">
        <li v-for="item in loaded.items" :key="item.producto_id">{{ item.cantidad }} × {{ item.nombre }} · {{ formatMoney(item.precio_centavos * item.cantidad) }}</li>
      </ul>
      <strong>Total de la cuenta: {{ formatMoney(loaded.total_centavos) }}</strong>
      <p>No es una factura ni acredita un pago.</p>
    </div>
  </section>
</template>
