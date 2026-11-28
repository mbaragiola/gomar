<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { request } from '../api'
import { localDateTime } from '../format'
import { printedReferenceLabel } from '../document-reference'
import type { RemovedDraft } from '../document-restore-handoff'

type RemovedDraftPage = { items: RemovedDraft[]; next_before_id?: number }

const props = defineProps<{ entityId: number; outletId: number; target?: RemovedDraft }>()
const emit = defineEmits<{ restored: [id: number] }>()
const endpoint = `/entities/${props.entityId}/outlets/${props.outletId}/removed-drafts`
const controller = new AbortController()
onBeforeUnmount(() => controller.abort())
const page = ref<RemovedDraftPage | null>(null)
const before = ref<number>()
const chosen = ref<RemovedDraft | null>(props.target ?? null)
const reasonField = ref<HTMLTextAreaElement | null>(null)
onMounted(() => { if (props.target) reasonField.value?.focus() })
const reason = ref('')
const busy = ref(false)
const error = ref('')
const status = ref('')
const requestOptions = { cache: 'no-store', signal: controller.signal } as const

async function load(cursor?: number) {
  busy.value = true
  error.value = ''
  chosen.value = null
  reason.value = ''
  try {
    page.value = await request<RemovedDraftPage>(cursor ? `${endpoint}?before_id=${cursor}` : endpoint, requestOptions)
    before.value = cursor
  } catch (err) {
    if (!controller.signal.aborted) error.value = err instanceof Error ? err.message : 'No se pudieron consultar los borradores.'
  } finally {
    busy.value = false
  }
}

async function restore() {
  if (!chosen.value || !reason.value.trim() || busy.value) return
  const draft = chosen.value
  busy.value = true
  error.value = ''
  status.value = ''
  try {
    await request(`${endpoint}/${draft.id}/restore`, {
      ...requestOptions,
      method: 'POST',
      body: JSON.stringify({ motivo: reason.value, expected_revision: draft.revision }),
    })
    if (controller.signal.aborted) return
    status.value = `${printedReferenceLabel(draft)} restaurada.`
    emit('restored', draft.id)
    if (!props.target) await load(before.value)
  } catch (err) {
    if (!controller.signal.aborted) error.value = err instanceof Error ? err.message : 'No se pudo restaurar el borrador.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <details class="document-reference-finder" :open="target ? true : undefined">
    <summary>Borradores dados de baja</summary>
    <button class="secondary" type="button" :disabled="busy" @click="load()">Buscar borradores dados de baja</button>
    <p v-if="status" role="status">{{ status }}</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <template v-if="page">
      <p v-if="!page.items.length">No hay borradores dados de baja en esta página.</p>
      <ul v-else>
        <li v-for="draft in page.items" :key="draft.id">
          <span>{{ printedReferenceLabel(draft) }} · CUIT {{ draft.issuer_cuit }} · {{ localDateTime(draft.removed_at) }}</span>
          <button
            class="text-button" type="button" :disabled="busy"
            :aria-label="`Seleccionar ${printedReferenceLabel(draft)} del CUIT ${draft.issuer_cuit}`"
            @click="chosen = draft; reason = ''"
          >Seleccionar</button>
        </li>
      </ul>
      <button v-if="before" class="secondary" type="button" :disabled="busy" @click="load()">Primera página</button>
      <button v-if="page.next_before_id" class="secondary" type="button" :disabled="busy" @click="load(page.next_before_id)">Siguiente página</button>
    </template>
    <form v-if="chosen" class="form-grid" @submit.prevent="restore">
      <p class="full">Restaurar {{ printedReferenceLabel(chosen) }} del CUIT {{ chosen.issuer_cuit }}</p>
      <label class="full">Motivo de la restauración<textarea ref="reasonField" v-model="reason" rows="2" maxlength="2000" required /></label>
      <div class="form-actions full">
        <button class="secondary" type="button" :disabled="busy" @click="chosen = null">Cancelar</button>
        <button class="primary" :disabled="busy || !reason.trim()">Restaurar borrador</button>
      </div>
    </form>
  </details>
</template>
