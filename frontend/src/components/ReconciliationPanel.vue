<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ApiError, request } from '../api'
import { localDate, localDateTime, money } from '../format'
import type { Outlet, ReconciliationCandidate, ReconciliationRow, ReconciliationRun } from '../types'

const props = defineProps<{ entityId: number; outlets: Outlet[]; selectedOutletId: number | null }>()
const emit = defineEmits<{ error: [value: unknown] }>()

const runs = ref<ReconciliationRun[]>([])
const current = ref<ReconciliationRun | null>(null)
const candidates = ref<ReconciliationCandidate[]>([])
const selectedOutlets = ref<number[]>([])
const matchReference = ref(false)
const file = ref<File | null>(null)
const headers = ref<{ indice: number; encabezado: string }[]>([])
const preview = ref<Record<string, unknown>[]>([])
const previewGeneration = ref<number | null>(null)
const side = ref<'payments' | 'cash_flow'>('payments')
const delimiter = ref(',')
const dateFormat = ref('%Y-%m-%d')
const decimalSeparator = ref('.')
const defaultCurrency = ref('ARS')
const defaultOutletId = ref<number | null>(null)
const discoveredOutletLabels = ref<string[]>([])
const fileInputKey = ref(0)
const candidatePage = ref(1)
const candidateTotal = ref(0)
const paymentMemberPage = ref(1)
const cashFlowMemberPage = ref(1)
const rows = ref<ReconciliationRow[]>([])
const rowPage = ref(1)
const rowTotal = ref(0)
const events = ref<{ action: string; occurred_at: string; run_revision: number }[]>([])
const eventPage = ref(1)
const eventTotal = ref(0)
const mapping = reactive<Record<string, number | ''>>({
  date: '', amount: '', category: '', description: '', payment_method: '', currency: '', outlet: '', reference: '', vat_rate: '', vat_amount: '',
})
const outletLabels = reactive<Record<string, number>>({})
const selectedPayment = reactive<Record<number, number | null>>({})
const selectedCash = reactive<Record<number, number | null>>({})
const acknowledgements = reactive<Record<number, boolean>>({})
const busy = ref(false)
let generation = 0
let uploadGeneration = 0
let candidateGeneration = 0
let rowGeneration = 0
let eventGeneration = 0
let pendingDetail: { token: number; entityId: number; runId: number } | null = null

function operationKey() {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`
}

function clearTransient(options: { preserveUpload?: boolean } = {}) {
  if (!options.preserveUpload) {
    uploadGeneration += 1
    file.value = null
    headers.value = []
    preview.value = []
    previewGeneration.value = null
    discoveredOutletLabels.value = []
    fileInputKey.value += 1
    Object.keys(mapping).forEach((key) => { mapping[key] = '' })
    Object.keys(outletLabels).forEach((key) => delete outletLabels[key])
  }
  candidates.value = []
  candidatePage.value = 1
  candidateTotal.value = 0
  paymentMemberPage.value = 1
  cashFlowMemberPage.value = 1
  rows.value = []
  rowPage.value = 1
  rowTotal.value = 0
  events.value = []
  eventPage.value = 1
  eventTotal.value = 0
  clearSelections()
}

function clearPreview() {
  uploadGeneration += 1
  preview.value = []
  previewGeneration.value = null
}

function resetFile(event: Event) {
  file.value = (event.target as HTMLInputElement).files?.[0] ?? null
  headers.value = []
  discoveredOutletLabels.value = []
  Object.keys(mapping).forEach((key) => { mapping[key] = '' })
  Object.keys(outletLabels).forEach((key) => delete outletLabels[key])
  clearPreview()
}

const canImport = computed(() => Boolean(
  file.value && preview.value.length && previewGeneration.value === uploadGeneration,
))

function clearSelections() {
  Object.keys(selectedPayment).forEach((key) => delete selectedPayment[Number(key)])
  Object.keys(selectedCash).forEach((key) => delete selectedCash[Number(key)])
  Object.keys(acknowledgements).forEach((key) => delete acknowledgements[Number(key)])
}

function isCurrent(token: number, entityId: number, runId?: number) {
  return token === generation && entityId === props.entityId && (runId === undefined || runId === current.value?.id)
}

function report(value: unknown) {
  emit('error', value instanceof ApiError ? value : new ApiError(0, 'Ocurrió un error inesperado.'))
}

async function loadRuns(refreshCurrent = true) {
  const token = generation
  const entityId = props.entityId
  try {
    const values = await request<ReconciliationRun[]>(`/entities/${entityId}/reconciliations`)
    if (!isCurrent(token, entityId)) return
    runs.value = values
    if (refreshCurrent && current.value) {
      const refreshed = values.find((item) => item.id === current.value?.id)
      if (refreshed) current.value = refreshed
    }
  } catch (value) { if (token === generation) report(value) }
}

async function createRun() {
  if (!selectedOutlets.value.length) return
  const token = ++generation
  const entityId = props.entityId
  busy.value = true
  try {
    const run = await request<ReconciliationRun>(`/entities/${entityId}/reconciliations`, {
      method: 'POST', body: JSON.stringify({ outlet_ids: selectedOutlets.value, match_reference: matchReference.value }),
    })
    if (!isCurrent(token, entityId)) {
      if (entityId === props.entityId) await loadRuns(false)
      return
    }
    current.value = run
    defaultOutletId.value = run.outlet_ids.includes(props.selectedOutletId ?? -1) ? props.selectedOutletId : run.outlet_ids[0] ?? null
    runs.value.unshift(run)
    clearTransient()
    await loadRuns()
  } catch (value) { if (isCurrent(token, entityId)) report(value) } finally { if (isCurrent(token, entityId)) busy.value = false }
}

async function openRun(run: ReconciliationRun, options: { preserveUpload?: boolean } = {}) {
  const token = ++generation
  const entityId = props.entityId
  pendingDetail = { token, entityId, runId: run.id }
  busy.value = false
  clearTransient(options)
  try {
    const detail = await request<ReconciliationRun>(`/entities/${entityId}/reconciliations/${run.id}`)
    if (token !== generation) return
    if (current.value?.id === run.id && detail.revision < current.value.revision) return
    const preservedDefaultOutletId = current.value?.id === run.id && (options.preserveUpload || file.value !== null)
      ? defaultOutletId.value
      : null
    current.value = detail
    defaultOutletId.value = preservedDefaultOutletId !== null && detail.outlet_ids.includes(preservedDefaultOutletId)
      ? preservedDefaultOutletId
      : detail.outlet_ids.includes(props.selectedOutletId ?? -1) ? props.selectedOutletId : detail.outlet_ids[0] ?? null
    if (detail.state !== 'draft') await Promise.all([loadCandidates(token), loadRows(token), loadEvents(token)])
  } catch (value) { if (token === generation) report(value) } finally {
    if (pendingDetail?.token === token) pendingDetail = null
  }
}

function leaveRun() {
  generation += 1
  current.value = null
  busy.value = false
  clearTransient()
}

function uploadForm(includeMapping = false) {
  const form = new FormData()
  if (file.value) form.append('file', file.value)
  form.append('delimiter', delimiter.value)
  if (includeMapping) {
    const selected = Object.fromEntries(Object.entries(mapping).filter((entry): entry is [string, number] => typeof entry[1] === 'number'))
    form.append('mapping', JSON.stringify(selected))
    form.append('outlet_mapping', JSON.stringify(outletLabels))
    form.append('date_format', dateFormat.value)
    form.append('decimal_separator', decimalSeparator.value)
    form.append('default_currency', defaultCurrency.value)
    if (!("outlet" in selected) && defaultOutletId.value) form.append('default_outlet_id', String(defaultOutletId.value))
  }
  return form
}

async function inspectFile() {
  if (!current.value || !file.value) return
  const token = generation
  const entityId = props.entityId
  const runId = current.value.id
  const uploadToken = uploadGeneration
  busy.value = true
  try {
    const result = await request<{ columnas: { indice: number; encabezado: string }[] }>(
      `/entities/${entityId}/reconciliations/${runId}/inspect`, { method: 'POST', body: uploadForm() },
    )
    if (!isCurrent(token, entityId, runId) || uploadToken !== uploadGeneration) return
    headers.value = result.columnas
    preview.value = []
    previewGeneration.value = null
  } catch (value) { if (isCurrent(token, entityId, runId) && uploadToken === uploadGeneration) report(value) } finally { if (isCurrent(token, entityId, runId)) busy.value = false }
}

async function previewFile() {
  if (!current.value || mapping.date === '' || mapping.amount === '') return
  const token = generation
  const entityId = props.entityId
  const runId = current.value.id
  const uploadToken = uploadGeneration
  busy.value = true
  try {
    const result = await request<{ rows: Record<string, unknown>[]; outlet_labels?: string[] }>(
      `/entities/${entityId}/reconciliations/${runId}/preview`, { method: 'POST', body: uploadForm(true) },
    )
    if (!isCurrent(token, entityId, runId) || uploadToken !== uploadGeneration) return
    discoveredOutletLabels.value = result.outlet_labels ?? []
    preview.value = result.rows
    previewGeneration.value = uploadToken
  } catch (value) { if (isCurrent(token, entityId, runId) && uploadToken === uploadGeneration) report(value) } finally { if (isCurrent(token, entityId, runId)) busy.value = false }
}

async function importFile() {
  if (!current.value || !canImport.value) return
  const token = generation
  const entityId = props.entityId
  const runId = current.value.id
  const uploadToken = uploadGeneration
  const form = uploadForm(true)
  form.append('side', side.value)
  form.append('expected_revision', String(current.value.revision))
  form.append('operation_key', operationKey())
  busy.value = true
  try {
    const result = await request<{ revision: number }>(
      `/entities/${entityId}/reconciliations/${runId}/sources`, { method: 'POST', body: form },
    )
    if (!isCurrent(token, entityId, runId) || !current.value) return
    current.value.revision = result.revision
    busy.value = false
    await openRun(current.value, { preserveUpload: uploadToken !== uploadGeneration })
  } catch (value) { if (isCurrent(token, entityId, runId)) report(value) } finally { if (isCurrent(token, entityId, runId)) busy.value = false }
}

async function prepareRun() {
  if (!current.value) return
  const token = generation
  const entityId = props.entityId
  const runId = current.value.id
  try {
    const result = await request<ReconciliationRun>(`/entities/${entityId}/reconciliations/${runId}/prepare`, {
      method: 'POST', body: JSON.stringify({ expected_revision: current.value.revision, operation_key: operationKey() }),
    })
    if (!isCurrent(token, entityId, runId)) return
    current.value = result
    await Promise.all([loadCandidates(generation), loadRows(generation), loadEvents(generation)])
  } catch (value) { if (isCurrent(token, entityId, runId)) report(value) }
}

async function loadCandidates(token = generation) {
  if (!current.value) return
  const requestToken = ++candidateGeneration
  const entityId = props.entityId
  const runId = current.value.id
  try {
    const result = await request<{ items: ReconciliationCandidate[]; total: number }>(
      `/entities/${entityId}/reconciliations/${runId}/candidates?page=${candidatePage.value}&page_size=20&payment_member_page=${paymentMemberPage.value}&cash_flow_member_page=${cashFlowMemberPage.value}&member_page_size=50`,
    )
    if (isCurrent(token, entityId, runId) && requestToken === candidateGeneration) {
      candidates.value = result.items
      candidateTotal.value = result.total
    }
  } catch (value) { if (isCurrent(token, entityId, runId) && requestToken === candidateGeneration) report(value) }
}

async function loadRows(token = generation) {
  if (!current.value) return
  const requestToken = ++rowGeneration
  const entityId = props.entityId
  const runId = current.value.id
  try {
    const result = await request<{ items: ReconciliationRow[]; total: number }>(`/entities/${entityId}/reconciliations/${runId}/rows?page=${rowPage.value}&page_size=25`)
    if (isCurrent(token, entityId, runId) && requestToken === rowGeneration) { rows.value = result.items; rowTotal.value = result.total }
  } catch (value) { if (isCurrent(token, entityId, runId) && requestToken === rowGeneration) report(value) }
}

async function loadEvents(token = generation) {
  if (!current.value) return
  const requestToken = ++eventGeneration
  const entityId = props.entityId
  const runId = current.value.id
  try {
    const result = await request<{ items: { action: string; occurred_at: string; run_revision: number }[]; total: number }>(`/entities/${entityId}/reconciliations/${runId}/events?page=${eventPage.value}&page_size=25`)
    if (isCurrent(token, entityId, runId) && requestToken === eventGeneration) { events.value = result.items; eventTotal.value = result.total }
  } catch (value) { if (isCurrent(token, entityId, runId) && requestToken === eventGeneration) report(value) }
}

async function decide(item: ReconciliationCandidate, decision: 'confirmed' | 'rejected') {
  if (!current.value || !item.payments[0] || !item.cash_flow[0]) return
  const paymentId = selectedPayment[item.group_id] ?? item.payments[0].id
  const cashFlowId = selectedCash[item.group_id] ?? item.cash_flow[0].id
  const token = generation
  const entityId = props.entityId
  const runId = current.value.id
  try {
    const result = await request<{ revision: number }>(`/entities/${entityId}/reconciliations/${runId}/decisions`, {
      method: 'POST', body: JSON.stringify({
        payment_row_id: paymentId,
        cash_flow_row_id: cashFlowId,
        decision,
        discrepancy_acknowledged: acknowledgements[item.group_id] === true,
        expected_revision: current.value.revision,
        operation_key: operationKey(),
      }),
    })
    if (!isCurrent(token, entityId, runId) || !current.value) return
    current.value.revision = result.revision
    await loadCandidates(generation)
  } catch (value) { if (isCurrent(token, entityId, runId)) report(value) }
}

async function closeRun() {
  if (!current.value || !window.confirm('¿Cerrar la conciliación con las filas sin resolver visibles?')) return
  const token = generation
  const entityId = props.entityId
  const runId = current.value.id
  try {
    const result = await request<ReconciliationRun>(`/entities/${entityId}/reconciliations/${runId}/close`, {
      method: 'POST', body: JSON.stringify({ expected_revision: current.value.revision, operation_key: operationKey() }),
    })
    if (isCurrent(token, entityId, runId)) current.value = result
  } catch (value) { if (isCurrent(token, entityId, runId)) report(value) }
}

async function deleteRun() {
  if (!current.value || !window.confirm('¿Eliminar los datos importados de esta conciliación?')) return
  const token = generation
  const entityId = props.entityId
  const runId = current.value.id
  try {
    await request(`/entities/${entityId}/reconciliations/${runId}?expected_revision=${current.value.revision}&operation_key=${operationKey()}`, { method: 'DELETE' })
    if (entityId !== props.entityId) return
    if (current.value?.id === runId) leaveRun()
    else if (pendingDetail?.entityId === entityId && pendingDetail.runId === runId) generation += 1
    runs.value = runs.value.filter((run) => run.id !== runId)
    await loadRuns(false)
  } catch (value) { if (isCurrent(token, entityId, runId)) report(value) }
}

async function deleteSource(sourceId: number) {
  if (!current.value) return
  const token = generation
  const entityId = props.entityId
  const runId = current.value.id
  try {
    const result = await request<{ revision: number }>(`/entities/${entityId}/reconciliations/${runId}/sources/${sourceId}?expected_revision=${current.value.revision}&operation_key=${operationKey()}`, { method: 'DELETE' })
    if (!isCurrent(token, entityId, runId) || !current.value) return
    current.value.revision = result.revision
    await openRun(current.value)
  } catch (value) { if (isCurrent(token, entityId, runId)) report(value) }
}

async function changeCandidatePage(delta: number) {
  candidatePage.value = Math.max(1, candidatePage.value + delta)
  paymentMemberPage.value = 1
  cashFlowMemberPage.value = 1
  clearSelections()
  await loadCandidates(generation)
}

async function changeMemberPage(sideValue: 'payments' | 'cash_flow', delta: number) {
  if (sideValue === 'payments') {
    paymentMemberPage.value = Math.max(1, paymentMemberPage.value + delta)
    Object.keys(selectedPayment).forEach((key) => delete selectedPayment[Number(key)])
  } else {
    cashFlowMemberPage.value = Math.max(1, cashFlowMemberPage.value + delta)
    Object.keys(selectedCash).forEach((key) => delete selectedCash[Number(key)])
  }
  Object.keys(acknowledgements).forEach((key) => delete acknowledgements[Number(key)])
  await loadCandidates(generation)
}

function selectedPairComparisons(item: ReconciliationCandidate) {
  const payment = item.payments.find((row) => row.id === (selectedPayment[item.group_id] ?? item.payments[0]?.id))
  const cash = item.cash_flow.find((row) => row.id === (selectedCash[item.group_id] ?? item.cash_flow[0]?.id))
  if (!payment || !cash) return []
  return [
    ['category', payment.category, cash.category],
    ['vat_rate', payment.vat_rate, cash.vat_rate],
    ['vat_amount', payment.vat_amount, cash.vat_amount],
  ].filter(([, left, right]) => left && right && left !== right).map(([field]) => field)
}

async function changeRowPage(delta: number) { rowPage.value = Math.max(1, rowPage.value + delta); await loadRows(generation) }
async function changeEventPage(delta: number) { eventPage.value = Math.max(1, eventPage.value + delta); await loadEvents(generation) }

watch(delimiter, () => {
  headers.value = []
  discoveredOutletLabels.value = []
  Object.keys(mapping).forEach((key) => { mapping[key] = '' })
  Object.keys(outletLabels).forEach((key) => delete outletLabels[key])
  clearPreview()
})

watch(
  [
    side,
    dateFormat,
    decimalSeparator,
    defaultCurrency,
    defaultOutletId,
    () => Object.entries(mapping),
    () => Object.entries(outletLabels),
  ],
  clearPreview,
  { deep: true },
)

watch(() => [props.entityId, props.selectedOutletId], () => {
  generation += 1
  candidateGeneration += 1
  rowGeneration += 1
  eventGeneration += 1
  current.value = null
  runs.value = []
  selectedOutlets.value = props.selectedOutletId ? [props.selectedOutletId] : []
  defaultOutletId.value = props.selectedOutletId
  busy.value = false
  clearTransient()
  void loadRuns()
})

onMounted(() => {
  selectedOutlets.value = props.selectedOutletId ? [props.selectedOutletId] : []
  void loadRuns()
})

onBeforeUnmount(() => {
  generation += 1
  candidateGeneration += 1
  rowGeneration += 1
  eventGeneration += 1
  clearTransient()
})
</script>

<template>
  <section class="card reconciliation-panel" aria-label="Conciliación financiera">
    <div class="section-heading"><div><p class="eyebrow">ADMINISTRACIÓN</p><h2>Conciliación financiera</h2></div><span>CSV · coincidencias exactas</span></div>
    <div v-if="!current" class="reconciliation-home">
      <div class="reconciliation-create">
        <h3>Nueva conciliación</h3>
        <fieldset><legend>Locales incluidos</legend><label v-for="outlet in outlets" :key="outlet.id" class="check"><input v-model="selectedOutlets" type="checkbox" :value="outlet.id" /> {{ outlet.nombre }}</label></fieldset>
        <label class="check"><input v-model="matchReference" type="checkbox" /> Usar una referencia común exacta en todos los archivos</label>
        <button class="primary" :disabled="busy || !selectedOutlets.length" @click="createRun">Crear conciliación</button>
      </div>
      <div><h3>Conciliaciones</h3><button v-for="run in runs" :key="run.id" class="reconciliation-run" @click="openRun(run)"><strong>#{{ run.id }} · {{ run.state }}</strong><span>{{ run.counts.rows }} filas · {{ run.unresolved_count ?? 'sin cerrar' }} sin resolver</span></button><p v-if="!runs.length" class="muted">No hay conciliaciones.</p></div>
    </div>
    <div v-else class="reconciliation-workspace">
      <div class="reconciliation-toolbar"><button class="text-button" @click="leaveRun">← Volver</button><strong>Conciliación #{{ current.id }}</strong><span class="status">{{ current.state }}</span><button class="danger" @click="deleteRun">Eliminar</button></div>
      <template v-if="current.state === 'draft'">
        <p class="muted">El archivo queda en memoria del navegador hasta importar. Los encabezados solo se usan en esta pantalla.</p>
        <div class="reconciliation-mapping">
          <label>Lado<select v-model="side"><option value="payments">Base de pagos</option><option value="cash_flow">Movimientos de caja</option></select></label>
          <label>Separador<select v-model="delimiter"><option value=",">Coma</option><option value=";">Punto y coma</option></select></label>
          <label class="file-picker">{{ file?.name ?? 'Elegir CSV' }}<input :key="fileInputKey" aria-label="Archivo CSV" type="file" accept=".csv,text/csv" @change="resetFile" /></label>
          <button class="secondary" :disabled="!file || busy" @click="inspectFile">Inspeccionar</button>
        </div>
        <div v-if="headers.length" class="reconciliation-mapping fields">
          <label v-for="(label, field) in { date: 'Fecha', amount: 'Importe', category: 'Categoría', description: 'Descripción', payment_method: 'Medio de pago', currency: 'Moneda', outlet: 'Local', reference: 'Referencia', vat_rate: 'Tasa IVA', vat_amount: 'Importe IVA' }" :key="field">{{ label }}<select v-model="mapping[field]"><option value="">No importar</option><option v-for="header in headers" :key="header.indice" :value="header.indice">{{ header.indice + 1 }} · {{ header.encabezado }}</option></select></label>
          <label>Formato de fecha<select v-model="dateFormat"><option value="%Y-%m-%d">AAAA-MM-DD</option><option value="%d/%m/%Y">DD/MM/AAAA</option><option value="%d-%m-%Y">DD-MM-AAAA</option></select></label>
          <label>Separador decimal<select v-model="decimalSeparator"><option value=".">Punto</option><option value=",">Coma</option></select></label>
          <label>Moneda si no está mapeada<select v-model="defaultCurrency"><option value="ARS">ARS</option><option value="USD">USD</option></select></label>
          <label v-if="mapping.outlet === ''">Local si no está mapeado<select v-model="defaultOutletId"><option v-for="outlet in outlets.filter((value) => current?.outlet_ids.includes(value.id))" :key="outlet.id" :value="outlet.id">{{ outlet.nombre }}</option></select></label>
          <label v-for="label in discoveredOutletLabels" :key="label">Resolver “{{ label }}”<select v-model="outletLabels[label]"><option disabled value="">Elegí un local</option><option v-for="outlet in outlets.filter((value) => current?.outlet_ids.includes(value.id))" :key="outlet.id" :value="outlet.id">{{ outlet.nombre }}</option></select></label>
          <div class="reconciliation-actions"><button class="secondary" @click="previewFile">Vista previa</button><button class="primary" :disabled="!canImport" @click="importFile">Importar</button></div>
        </div>
        <div v-if="preview.length" class="reconciliation-table"><table><thead><tr><th>Registro</th><th>Local resuelto</th><th>Fecha</th><th>Importe</th><th>Moneda</th><th>Categoría</th><th>Descripción</th><th>Medio</th><th>Referencia</th><th>Tasa IVA</th><th>Importe IVA</th></tr></thead><tbody><tr v-for="row in preview" :key="String(row.record_number)"><td>{{ row.record_number }}</td><td>{{ outlets.find((value) => value.id === row.outlet_id)?.nombre }}</td><td>{{ row.date }}</td><td>{{ row.amount }}</td><td>{{ row.currency }} · {{ row.currency_source === 'imported' ? 'importada' : 'suministrada' }}</td><td>{{ row.category || 'No suministrado' }}</td><td>{{ row.description || 'No suministrado' }}</td><td>{{ row.payment_method || 'No suministrado' }}</td><td>{{ row.reference || 'No suministrado' }}</td><td>{{ row.vat_rate || 'No comparable' }}</td><td>{{ row.vat_amount || 'No comparable' }}</td></tr></tbody></table></div>
        <div class="reconciliation-sources"><span v-for="source in current.sources" :key="source.id">{{ source.label }} · {{ source.row_count }} filas <button class="text-button" @click="deleteSource(source.id)">Quitar</button></span></div>
        <button class="primary" :disabled="(current.sources?.length ?? 0) < 2" @click="prepareRun">Preparar revisión</button>
      </template>
      <template v-else>
        <p v-if="current.state === 'closed' && current.unresolved_count" class="error">Cerrada con {{ current.unresolved_count }} filas sin resolver.</p>
        <div v-for="item in candidates" :key="item.group_id" class="reconciliation-candidate">
          <strong>{{ item.kind === 'candidate' ? 'Candidata exacta' : item.kind === 'ambiguous' ? 'Grupo ambiguo' : 'Sin contraparte' }}</strong>
          <div class="reconciliation-sides"><div><h3>Base de pagos ({{ item.payment_count }})</h3><label v-for="row in item.payments" :key="row.id" class="reconciliation-row"><input v-if="item.kind === 'ambiguous'" v-model="selectedPayment[item.group_id]" type="radio" :value="row.id" /> <span>{{ localDate(row.date) }} · {{ money(row.amount, row.currency) }} ({{ row.currency_source === 'imported' ? 'importada' : 'suministrada' }}) · {{ outlets.find((value) => value.id === row.outlet_id)?.nombre }} · {{ row.source }} #{{ row.record_number }}<br />Categoría: {{ row.category || 'no suministrada' }} · IVA: {{ row.vat_rate || 'no comparable' }} / {{ row.vat_amount ? money(row.vat_amount, row.currency) : 'no comparable' }}<br />{{ row.description || 'Descripción no suministrada' }} · {{ row.payment_method || 'Medio no suministrado' }} · Ref. {{ row.reference || 'no suministrada' }}</span></label></div><div><h3>Movimientos de caja ({{ item.cash_flow_count }})</h3><label v-for="row in item.cash_flow" :key="row.id" class="reconciliation-row"><input v-if="item.kind === 'ambiguous'" v-model="selectedCash[item.group_id]" type="radio" :value="row.id" /> <span>{{ localDate(row.date) }} · {{ money(row.amount, row.currency) }} ({{ row.currency_source === 'imported' ? 'importada' : 'suministrada' }}) · {{ outlets.find((value) => value.id === row.outlet_id)?.nombre }} · {{ row.source }} #{{ row.record_number }}<br />Categoría: {{ row.category || 'no suministrada' }} · IVA: {{ row.vat_rate || 'no comparable' }} / {{ row.vat_amount ? money(row.vat_amount, row.currency) : 'no comparable' }}<br />{{ row.description || 'Descripción no suministrada' }} · {{ row.payment_method || 'Medio no suministrado' }} · Ref. {{ row.reference || 'no suministrada' }}</span></label></div></div>
          <p v-if="item.discrepancies?.length" class="error">Diferencias: {{ item.discrepancies.join(', ') }}. La confirmación reconoce esta revisión.</p>
          <p v-if="item.kind === 'ambiguous' && selectedPairComparisons(item).length" class="error">Diferencias del par elegido: {{ selectedPairComparisons(item).join(', ') }}.</p>
          <p v-if="item.comparisons && Object.values(item.comparisons).includes('not_comparable')" class="muted">Hay datos de categoría o IVA no suministrados; no son comparables.</p>
          <label v-if="item.kind === 'ambiguous' || item.discrepancies?.length" class="check"><input v-model="acknowledgements[item.group_id]" type="checkbox" /> Revisé categoría e IVA del par elegido y reconozco cualquier diferencia.</label>
          <p v-for="decision in item.decisions" :key="`${decision.payment_row_id}-${decision.cash_flow_row_id}`" class="status">Par {{ decision.payment_row_id }}/{{ decision.cash_flow_row_id }}: {{ decision.decision }}</p>
          <div v-if="current.state === 'review' && item.payments.length && item.cash_flow.length" class="reconciliation-actions"><button class="secondary" :disabled="item.kind === 'ambiguous' && (!selectedPayment[item.group_id] || !selectedCash[item.group_id])" @click="decide(item, 'rejected')">Rechazar par</button><button v-if="item.kind !== 'ambiguous'" class="primary" :disabled="Boolean(item.discrepancies?.length) && !acknowledgements[item.group_id]" @click="decide(item, 'confirmed')">Confirmar par</button></div>
        </div>
        <div class="reconciliation-actions"><button class="secondary" :disabled="candidatePage === 1" @click="changeCandidatePage(-1)">Grupos anteriores</button><span>Página {{ candidatePage }} · {{ candidateTotal }} grupos</span><button class="secondary" :disabled="candidatePage * 20 >= candidateTotal" @click="changeCandidatePage(1)">Grupos siguientes</button></div>
        <div v-if="candidates.some((item) => item.payment_count > 50)" class="reconciliation-actions"><button class="secondary" :disabled="paymentMemberPage === 1" @click="changeMemberPage('payments', -1)">Pagos anteriores</button><span>Página de pagos {{ paymentMemberPage }}</span><button class="secondary" :disabled="!candidates.some((item) => item.payment_count > paymentMemberPage * 50)" @click="changeMemberPage('payments', 1)">Pagos siguientes</button></div>
        <div v-if="candidates.some((item) => item.cash_flow_count > 50)" class="reconciliation-actions"><button class="secondary" :disabled="cashFlowMemberPage === 1" @click="changeMemberPage('cash_flow', -1)">Caja anterior</button><span>Página de caja {{ cashFlowMemberPage }}</span><button class="secondary" :disabled="!candidates.some((item) => item.cash_flow_count > cashFlowMemberPage * 50)" @click="changeMemberPage('cash_flow', 1)">Caja siguiente</button></div>
        <div class="subsection"><h3>Filas importadas</h3><p v-for="row in rows" :key="row.id">{{ row.source }} #{{ row.record_number }} · {{ outlets.find((value) => value.id === row.outlet_id)?.nombre }} · {{ localDate(row.date) }} · {{ money(row.amount, row.currency) }}</p><div class="reconciliation-actions"><button class="secondary" :disabled="rowPage === 1" @click="changeRowPage(-1)">Filas anteriores</button><span>{{ rowTotal }} filas</span><button class="secondary" :disabled="rowPage * 25 >= rowTotal" @click="changeRowPage(1)">Filas siguientes</button></div></div>
        <div class="subsection"><h3>Historial de la conciliación</h3><p v-for="event in events" :key="`${event.run_revision}-${event.action}`">Revisión {{ event.run_revision }} · {{ event.action }} · {{ localDateTime(event.occurred_at) }}</p><div class="reconciliation-actions"><button class="secondary" :disabled="eventPage === 1" @click="changeEventPage(-1)">Eventos anteriores</button><span>{{ eventTotal }} eventos</span><button class="secondary" :disabled="eventPage * 25 >= eventTotal" @click="changeEventPage(1)">Eventos siguientes</button></div></div>
        <button v-if="current.state === 'review'" class="primary" @click="closeRun">Cerrar y conservar pendientes</button>
      </template>
    </div>
  </section>
</template>
