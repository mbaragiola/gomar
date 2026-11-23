<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, request } from '../api'
import ManualTender from './ManualTender.vue'
import type { Context, Line } from './model'

const props = defineProps<{
  scope: Context
  cart: ReadonlyArray<Line>
  online: boolean
  allowed: () => boolean
  formatMoney: (amount: number) => string
  allowTender?: boolean
}>()

interface Check {
  id_cuenta: string
  estado: "sin_cobro" | "retirada" | "con_registros"
  mesa: number
  total_centavos: number
  items: Line[]
}

const number = ref('')
const loaded = ref<Check | null>(null)
const error = ref('')
const retiring = ref(false)
const reason = ref('')
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
  if (!active || !props.allowed() || !props.online || !table.value || busy.value || save && !props.cart.length) return
  const picked = table.value
  const path = `/entities/${props.scope.entity}/outlets/${props.scope.outlet}/pos/table-checks`
  const data = { mesa: picked, items: props.cart.map(({ producto_id, revision, cantidad }) => ({ producto_id, revision, cantidad })) }
  const payload = JSON.stringify(data)
  const next = new AbortController()
  controller = next
  busy.value = true
  loaded.value = null
  retiring.value = false
  reason.value = ''
  error.value = ''
  try {
    if (save && retry?.payload !== payload) retry = { payload, id: crypto.randomUUID() }
    const response = await request<Check>(save ? path : path + '/' + picked + (retire ? '/retire' : ''), {
      ...(save ? { method: 'POST', body: JSON.stringify({ ...data, id_solicitud: retry?.id }) }
        : retire ? { method: 'POST', body: JSON.stringify({ id_cuenta: reviewed?.id_cuenta, motivo: motive }) } : {}),
      cache: 'no-store', signal: AbortSignal.any([next.signal, AbortSignal.timeout(8000)]),
    })
    if (!active || next.signal.aborted || !props.allowed() || table.value !== picked) return
    if (response.mesa !== picked || !Number.isSafeInteger(response.total_centavos) || response.total_centavos < 1
      || typeof response.id_cuenta !== 'string' || !/^[1-9][0-9]{0,18}$/.test(response.id_cuenta) || !['sin_cobro', 'retirada', 'con_registros'].includes(response.estado)
      || retire && (response.id_cuenta !== reviewed?.id_cuenta || response.estado !== 'retirada')
      || !Array.isArray(response.items) || !response.items.length) throw new Error('Respuesta no válida')
    // A confirmed retirement ends the old save intent. A later deliberate
    // click can save the same cart for the next use of this table.
    if ((save || retire) && response.estado === 'retirada') retry = undefined
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
    <p>Podés retirar una cuenta guardada por error o cancelada. Eso libera la mesa sin cobrar ni facturar.</p>
    <p v-if="!online">Conectate para guardar o consultar cuentas.</p>
    <label>Número de mesa
      <input v-model="number" type="text" inputmode="numeric" pattern="[1-9][0-9]{0,3}" maxlength="4" :disabled="!online || busy" @input="changeTable" />
    </label>
    <div class="gomar-feature-actions">
      <button type="button" :disabled="!online || busy || !table" @click="act('read')">Consultar mesa</button>
      <button type="button" :disabled="!online || busy || !table || !cart.length" @click="act('save')">Guardar cuenta sin cobro</button>
    </div>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <div v-if="loaded && online" aria-live="polite">
      <h3 v-if="loaded.estado === 'retirada'">Cuenta retirada: mesa {{ loaded.mesa }}</h3>
      <h3 v-else>Cuenta guardada: mesa {{ loaded.mesa }}</h3>
      <ul aria-label="Productos de la cuenta guardada">
        <li v-for="item in loaded.items" :key="item.producto_id">{{ item.cantidad }} × {{ item.nombre }} · {{ formatMoney(item.precio_centavos * item.cantidad) }}</li>
      </ul>
      <strong>Total de la cuenta: {{ formatMoney(loaded.total_centavos) }}</strong>
      <p>No es una factura ni acredita un pago.</p>
      <ManualTender
        v-if="allowTender && loaded.estado !== 'retirada'" 
        :key="loaded.id_cuenta" :scope="scope" :table="loaded.mesa" :check="loaded.id_cuenta"
        :total="loaded.total_centavos" :allowed="allowed" :format-money="formatMoney"
        @has-history="loaded.estado = 'con_registros'"
      />
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
