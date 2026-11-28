<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ApiError, request } from '../api'
import { isPaymentMethod, maxTotal, paymentLabels, type Context, type PaymentMethod } from './model'

const props = defineProps<{
  scope: Context
  table: number
  check: string
  total: number
  allowed: () => boolean
  allowFinalization: boolean
  cartEmpty: boolean
  formatMoney: (amount: number) => string
}>()
const emit = defineEmits<{ activeTender: [active: boolean]; finalized: [] }>()
const reasons = { entrada_incorrecta: 'Registro por error', medio_incorrecto: 'Medio incorrecto', importe_incorrecto: 'Importe incorrecto' } as const
type Reason = keyof typeof reasons
interface Entry {
  id_registro: string
  tipo: 'record' | 'void'
  medio?: PaymentMethod
  importe_centavos?: number
  registro_original?: string
  motivo?: Reason
}
interface Status {
  id_cuenta: string
  registrado_centavos: number
  saldo_centavos: number
  registros: Entry[]
}
const amount = ref('')
const method = ref<PaymentMethod | ''>('')
const correcting = ref('')
const reason = ref<Reason | ''>('')
const status = ref<Status>()
const error = ref('')
const busy = ref(false)
const uncertain = ref(false)
const confirming = ref(false)
const finalRetry = ref<string>()
const candidateRevision = computed(() => status.value?.registros.at(-1)?.id_registro)
let retry: { data: string; id: string } | undefined
const controller = new AbortController()
const path = `/entities/${props.scope.entity}/outlets/${props.scope.outlet}/pos/table-checks/${props.table}/tender`
const corrected = computed(() => new Set(status.value?.registros.filter((row) => row.tipo === 'void').map((row) => row.registro_original)))
const parsedAmount = computed(() => {
  const match = /^([0-9]{1,9})(?:,([0-9]{1,2}))?$/.exec(amount.value)
  return match ? Number(match[1]) * 100 + Number((match[2] ?? '').padEnd(2, '0')) : 0
})
const validID = (value: unknown): value is string => typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value)
const permitted = () => !controller.signal.aborted && props.allowed()
const signal = () => AbortSignal.any([controller.signal, AbortSignal.timeout(8000)])

function readStatus(value: Status) {
  if (value.id_cuenta !== props.check || !Number.isSafeInteger(value.registrado_centavos) || value.registrado_centavos < 0
    || !Number.isSafeInteger(value.saldo_centavos) || value.saldo_centavos < 0
    || value.registrado_centavos + value.saldo_centavos !== props.total || !Array.isArray(value.registros) || value.registros.length > 1000) {
    throw new Error('unexpected tender response')
  }
  const records = new Map<string, number>()
  const all = new Set<string>()
  let total = 0
  for (const entry of value.registros) {
    if (!validID(entry.id_registro) || all.has(entry.id_registro)) throw new Error('unexpected tender history')
    all.add(entry.id_registro)
    if (entry.tipo === 'record' && isPaymentMethod(entry.medio) && Number.isSafeInteger(entry.importe_centavos)
      && entry.importe_centavos! > 0 && entry.importe_centavos! <= maxTotal) {
      records.set(entry.id_registro, entry.importe_centavos!)
      total += entry.importe_centavos!
    } else if (entry.tipo === 'void' && validID(entry.registro_original) && records.has(entry.registro_original)
      && entry.motivo && Object.hasOwn(reasons, entry.motivo)) {
      total -= records.get(entry.registro_original)!
      records.delete(entry.registro_original)
    } else {
      throw new Error('unexpected tender history')
    }
    if (total < 0 || total > props.total) throw new Error('unexpected tender balance')
  }
  if (total !== value.registrado_centavos) throw new Error('unexpected tender balance')
  status.value = value
  emit('activeTender', value.registrado_centavos > 0)
}

async function load() {
  if (!permitted()) return
  const view = await request<Status>(path, { cache: 'no-store', signal: signal() })
  if (permitted()) readStatus(view)
}

async function refresh() {
  if (busy.value || !permitted()) return
  busy.value = true
  error.value = ''
  try {
    await load()
    // Explicit readback is the way to inspect an uncertain mutation.
    uncertain.value = false
  } catch (cause) {
    if (permitted()) error.value = cause instanceof ApiError ? cause.message : 'No se pudo consultar el registro de pagos.'
  } finally {
    busy.value = false
  }
}

async function save(kind: 'record' | 'void') {
  if (busy.value || uncertain.value || !status.value || !permitted()) return
  if (kind === 'record' && (!isPaymentMethod(method.value) || parsedAmount.value <= 0 || parsedAmount.value > status.value.saldo_centavos)) return
  if (kind === 'void' && (!reason.value || !validID(correcting.value) || corrected.value.has(correcting.value))) return
  const command = { id_cuenta: props.check, tipo: kind, ...(kind === 'record' ? { medio: method.value, importe_centavos: parsedAmount.value }
    : { motivo: reason.value, registro_original: correcting.value }) }
  const data = JSON.stringify(command)
  if (retry?.data !== data) retry = { data, id: crypto.randomUUID() }
  busy.value = true
  error.value = ''
  try {
    const receipt = await request<{ id_registro: string; id_cuenta: string }>(path, {
      method: 'POST', body: JSON.stringify({ ...command, id_solicitud: retry.id }), cache: 'no-store', signal: signal(),
    })
    if (!permitted()) return
    if (!validID(receipt.id_registro) || receipt.id_cuenta !== props.check) throw new Error('unexpected tender receipt')
    emit('activeTender', true)
    retry = undefined
    amount.value = method.value = correcting.value = reason.value = ''
    await load()
  } catch (cause) {
    if (permitted()) {
      uncertain.value = true
      error.value = cause instanceof ApiError ? cause.message : 'No se pudo verificar el registro. Actualizá antes de volver a intentar.'
    }
  } finally {
    busy.value = false
  }
}

async function finalize() {
  if (!props.allowFinalization || !props.cartEmpty || !confirming.value || busy.value
    || !status.value || status.value.saldo_centavos !== 0 || !candidateRevision.value || !permitted()) return
  const command = { id_cuenta: props.check, revision_pagos: candidateRevision.value,
    id_solicitud: finalRetry.value ?? crypto.randomUUID() }
  finalRetry.value = command.id_solicitud
  busy.value = true
  error.value = ''
  try {
    const receipt = await request<{ id_cuenta: string; id_venta: string; total_centavos: number }>(
      path.replace(/tender$/, 'finalize'), { method: 'POST', body: JSON.stringify(command), cache: 'no-store', signal: signal() },
    )
    if (!permitted()) return
    if (receipt.id_cuenta !== props.check || !validID(receipt.id_venta) || receipt.total_centavos !== props.total) {
      throw new Error('unexpected sale receipt')
    }
    emit('finalized')
  } catch (cause) {
    if (!permitted()) return
    if (cause instanceof ApiError && cause.status >= 400 && cause.status < 500) {
      confirming.value = false
      finalRetry.value = undefined
      uncertain.value = true
      error.value = cause.message
    } else {
      error.value = 'No se pudo verificar la venta. Reintentá la misma confirmación antes de volver a cargarla.'
    }
  } finally {
    busy.value = false
  }
}

onMounted(refresh)
onUnmounted(() => controller.abort())
</script>

<template>
  <section class="card gomar-feature" aria-label="Registro de medios de pago">
    <h4>Registro de medios de pago</h4>
    <p>Anotá solo importes gestionados por fuera de Gomar. Esto no cobra, reintegra, registra una venta ni emite una factura.</p>
    <button type="button" :disabled="busy || confirming" @click="refresh">Actualizar registros de pago</button>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <template v-if="status">
      <p role="status">Registrado: {{ formatMoney(status.registrado_centavos) }} · Saldo por registrar: {{ formatMoney(status.saldo_centavos) }}</p>
      <ul aria-label="Historial de registros de pago">
        <li v-for="entry in status.registros" :key="entry.id_registro">
          <template v-if="entry.tipo === 'record' && entry.medio && entry.importe_centavos">
            {{ paymentLabels[entry.medio] }} · {{ formatMoney(entry.importe_centavos) }}
            <span v-if="corrected.has(entry.id_registro)"> · Corregido</span>
            <button v-else type="button" :disabled="busy || uncertain || confirming" @click="correcting = entry.id_registro; reason = ''">Corregir registro</button>
          </template>
          <span v-else-if="entry.motivo">Corrección: {{ reasons[entry.motivo] }}</span>
        </li>
      </ul>
      <form v-if="correcting" class="gomar-feature-actions" @submit.prevent="save('void')">
        <p>Corregir esta anotación no devuelve dinero. El registro original queda en el historial.</p>
        <label>Motivo de la corrección
          <select v-model="reason" :disabled="busy || uncertain">
            <option value="">Elegí un motivo</option>
            <option v-for="(label, code) in reasons" :key="code" :value="code">{{ label }}</option>
          </select>
        </label>
        <button type="submit" :disabled="!reason || busy || uncertain">Confirmar corrección</button>
        <button type="button" :disabled="busy" @click="correcting = ''; reason = ''">Volver sin corregir</button>
      </form>
      <div v-if="allowFinalization && status.saldo_centavos === 0 && !correcting">
        <p v-if="!cartEmpty">Vaciá el carrito local antes de registrar esta cuenta para no cargar la venta dos veces.</p>
        <button v-if="!confirming" type="button" :disabled="busy || uncertain || !cartEmpty" @click="confirming = true">Revisar y registrar venta</button>
        <fieldset v-else>
          <legend>Confirmá la venta de la mesa {{ table }}</legend>
          <p>Revisá los medios de pago y el total de {{ formatMoney(total) }}. Se registra una venta y se libera la mesa. No se cobra ni se emite una factura.</p>
          <button type="button" :disabled="busy || !cartEmpty" @click="finalize">{{ finalRetry ? 'Reintentar confirmación' : 'Confirmar venta' }}</button>
          <button v-if="!finalRetry" type="button" :disabled="busy" @click="confirming = false">Volver sin registrar</button>
        </fieldset>
      </div>
      <form v-if="status.saldo_centavos > 0 && !correcting" class="gomar-feature-actions" @submit.prevent="save('record')">
        <label>Importe ingresado
          <input v-model="amount" inputmode="decimal" placeholder="0,00" :disabled="busy || uncertain" />
        </label>
        <label>Medio usado
          <select v-model="method" :disabled="busy || uncertain">
            <option value="">Elegí un medio</option>
            <option v-for="(label, code) in paymentLabels" :key="code" :value="code">{{ label }}</option>
          </select>
        </label>
        <button type="submit" :disabled="!method || !parsedAmount || parsedAmount > status.saldo_centavos || busy || uncertain">Anotar importe</button>
      </form>
    </template>
  </section>
</template>
