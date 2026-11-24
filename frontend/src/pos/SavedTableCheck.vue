<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ApiError, request } from '../api'
import ManualTender from './ManualTender.vue'
import { isTableCheck, type TableCheck } from './tableSave'
import type { Context } from './model'

const props = defineProps<{
  scope: Context
  cartReady: boolean
  cartEmpty: boolean
  pendingTable?: number
  save: (table: number) => Promise<TableCheck | undefined>
  online: boolean
  allowed: () => boolean
  formatMoney: (amount: number) => string
  allowTender?: boolean
  allowFinalization: boolean
}>()

const number = ref('')
const loaded = ref<TableCheck | null>(null)
const error = ref('')
const retiring = ref(false)
const reason = ref('')
const busy = ref(false)
let controller: AbortController | undefined
let active = true
const table = computed(() => /^[1-9][0-9]{0,3}$/.test(number.value) ? Number(number.value) : 0)

function linkTable() {
  const m = /^#entity=([1-9][0-9]*)&outlet=([1-9][0-9]*)&table=([1-9][0-9]{0,3})$/.exec(window.location.hash)
  if (busy.value || props.pendingTable || !m || !props.allowed() || Number(m[1]) !== props.scope.entity || Number(m[2]) !== props.scope.outlet) return
  controller?.abort()
  number.value = m[3]
  error.value = ''
  retiring.value = false
  reason.value = ''
  loaded.value = null
}

function changeTable() {
  error.value = ''
  retiring.value = false
  reason.value = ''
  loaded.value = null
}

async function act(action: "save" | "read" | "retire") {
  const save = action === "save"
  const retire = action === "retire"
  const reviewed = loaded.value
  const motive = reason.value
  if (retire && (!retiring.value || reviewed?.estado !== "sin_cobro" || !motive)) return
  if (!active || !props.online || busy.value) return
  const picked = props.pendingTable ?? table.value
  if (!picked || (save ? !props.pendingTable && (!props.allowed() || !props.cartReady) : !props.allowed())) return
  busy.value = true
  loaded.value = null
  retiring.value = false
  reason.value = ''
  error.value = ''
  if (save) {
    try {
      const response = await props.save(picked)
      if (active && response && table.value === picked) loaded.value = response
    } finally {
      busy.value = false
    }
    return
  }
  const path = `/entities/${props.scope.entity}/outlets/${props.scope.outlet}/pos/table-checks/${picked}`
  const next = new AbortController()
  controller = next
  try {
    const response = await request<TableCheck>(path + (retire ? '/retire' : ''), {
      ...(retire ? { method: 'POST', body: JSON.stringify({ id_cuenta: reviewed?.id_cuenta, motivo: motive }) } : {}),
      cache: 'no-store', signal: AbortSignal.any([next.signal, AbortSignal.timeout(8000)]),
    })
    if (!active || next.signal.aborted || !props.allowed() || table.value !== picked) return
    if (!isTableCheck(response, picked)
      || retire && (response.id_cuenta !== reviewed?.id_cuenta || response.estado !== 'retirada')) throw new Error('Respuesta no válida')
    loaded.value = response
  } catch (err) {
    if (!active || next.signal.aborted || !props.allowed() || table.value !== picked) return
    error.value = err instanceof ApiError ? err.message : 'No se pudo verificar la cuenta. Consultá la mesa antes de volver a actuar.'
  } finally {
    if (controller === next) {
      controller = undefined
      busy.value = false
    }
  }
}

watch(() => props.pendingTable, (pending) => {
  if (pending) {
    number.value = String(pending)
    changeTable()
  }
}, { immediate: true })

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
    <p>Al guardar, los productos pasan del carrito a la cuenta de mesa. Podés consultarla en otra caja del mismo local. No se registra una venta ni un pago.</p>
    <p>Podés retirar una cuenta guardada por error o cancelada. Eso libera la mesa sin cobrar ni facturar.</p>
    <p v-if="!online">Conectate para guardar o consultar cuentas.</p>
    <label>Número de mesa
      <input v-model="number" type="text" inputmode="numeric" pattern="[1-9][0-9]{0,3}" maxlength="4" :disabled="!online || busy || !!pendingTable" @input="changeTable" />
    </label>
    <div class="gomar-feature-actions">
      <button type="button" :disabled="!online || busy || !!pendingTable || !table" @click="act('read')">Consultar mesa</button>
      <button type="button" :disabled="!online || busy || (!pendingTable && (!table || !cartReady))" @click="act('save')">
        {{ pendingTable && !busy ? 'Reintentar guardar la misma cuenta' : 'Guardar cuenta sin cobro' }}
      </button>
    </div>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <div v-if="loaded && online" aria-live="polite">
      <h3 v-if="loaded.estado === 'retirada'">Cuenta retirada: mesa {{ loaded.mesa }}</h3>
      <h3 v-else-if="loaded.estado === 'finalizada'">Venta registrada: mesa {{ loaded.mesa }}</h3>
      <h3 v-else>Cuenta guardada: mesa {{ loaded.mesa }}</h3>
      <ul aria-label="Productos de la cuenta guardada">
        <li v-for="item in loaded.items" :key="item.producto_id">{{ item.cantidad }} × {{ item.nombre }} · {{ formatMoney(item.precio_centavos * item.cantidad) }}</li>
      </ul>
      <strong>Total de la cuenta: {{ formatMoney(loaded.total_centavos) }}</strong>
      <p>No es una factura ni acredita un pago.</p>
      <ManualTender
        v-if="allowTender && (loaded.estado === 'sin_cobro' || loaded.estado === 'con_registros')"
        :key="loaded.id_cuenta" :scope="scope" :table="loaded.mesa" :check="loaded.id_cuenta"
        :total="loaded.total_centavos" :allowed="allowed" :format-money="formatMoney"
        :allow-finalization="allowFinalization" :cart-empty="cartEmpty"
        @has-history="loaded.estado = 'con_registros'"
        @finalized="loaded.estado = 'finalizada'"
      />
      <p v-if="loaded.estado === 'finalizada'" role="status">La mesa quedó libre. Esta acción no emite una factura.</p>
      <p v-if="loaded.estado === 'con_registros'">No se puede retirar una cuenta con registros de pago.</p>
      <template v-if="loaded.estado === 'sin_cobro'">
        <button v-if="!retiring" type="button" :disabled="busy" @click="retiring = true">Retirar cuenta sin cobro</button>
        <fieldset v-else>
          <legend>Confirmá el retiro</legend>
          <p>Retirala solo si se guardó por error o se canceló. Si hubo consumo o pago, no uses esta opción.</p>
          <label>Motivo del retiro
            <select v-model="reason">
              <option disabled value="">Elegí un motivo</option>
              <option value="saved_in_error">Se guardó por error</option>
              <option value="cancelled">Se canceló sin consumo ni pago</option>
            </select>
          </label>
          <button type="button" :disabled="!reason || busy" @click="act('retire')">Confirmar retiro</button>
          <button type="button" @click="retiring = false; reason = ''">Volver</button>
        </fieldset>
      </template>
    </div>
  </section>
</template>
