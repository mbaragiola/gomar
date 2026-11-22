<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ApiError, request } from '../api'
import { todayInBuenosAires } from '../format'

interface Venue {
  venue: string
  display_name: string
  locale: string
  timezone: string
  cancellation_policy: string
  menu_pdf_url?: string
}

const props = defineProps<{ venue: string }>()
const path = `/public/reservas/${encodeURIComponent(props.venue)}`
const venueData = ref<Venue | null>(null)
const menuPath = computed(() => {
  const expected = `/api/v1${path}/menu.pdf`
  return venueData.value?.menu_pdf_url === expected ? expected : ''
})
const date = ref(todayInBuenosAires())
const size = ref(2)
const guest = ref('')
const slots = ref<string[]>([])
const slot = ref('')
const searched = ref(false)
const busy = ref(true)
const problem = ref('')
const confirmation = ref('')
const submittedPolicy = ref<string | null>(null)

function showError(err: unknown, fallback: string) {
  problem.value = err instanceof ApiError ? err.message : fallback
}

function resetSlots() {
  slots.value = []
  slot.value = ''
  searched.value = false
  confirmation.value = ''
  submittedPolicy.value = null
}

function localTime(value: string) {
  return new Intl.DateTimeFormat('es-AR', {
    timeZone: venueData.value?.timezone ?? 'America/Argentina/Buenos_Aires',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  }).format(new Date(value))
}

async function availability() {
  if (busy.value) return
  busy.value = true
  problem.value = ''
  resetSlots()
  try {
    const query = new URLSearchParams({ date: date.value, party_size: String(size.value) })
    const result = await request<{ slots: { start_at: string }[] }>(`${path}/availability?${query}`)
    slots.value = result.slots.map((entry) => entry.start_at)
    searched.value = true
  } catch (err) {
    showError(err, 'No se pudieron consultar los horarios.')
  } finally {
    busy.value = false
  }
}

async function book() {
  if (busy.value || !slot.value || !guest.value.trim()) return
  busy.value = true
  problem.value = ''
  try {
    const result = await request<{ id: string; status: 'pending'; cancellation_policy?: string }>(`${path}/requests`, {
      method: 'POST',
      body: JSON.stringify({ guest_name: guest.value.trim(), party_size: size.value, start_at: slot.value }),
    })
    resetSlots()
    guest.value = ''
    submittedPolicy.value = result.cancellation_policy ?? null
    confirmation.value = `Solicitud recibida. Todavía no está confirmada. Referencia: ${result.id}.`
  } catch (err) {
    showError(err, 'No se pudo solicitar la reserva.')
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  try {
    venueData.value = await request<Venue>(path)
  } catch (err) {
    showError(err, 'No se pudieron cargar las reservas.')
  } finally {
    busy.value = false
  }
})
</script>

<template>
  <main class="workspace gomar-public" aria-label="Reservas">
    <section class="card gomar-feature">
      <div><p class="eyebrow">RESERVAS</p><h1>{{ venueData?.display_name || 'Reservá tu mesa' }}</h1></div>
      <p v-if="problem" class="error" role="alert">{{ problem }}</p>
      <p v-if="confirmation" role="status">{{ confirmation }}</p>
      <p v-if="submittedPolicy" class="gomar-feature-text">Política al solicitar: {{ submittedPolicy }}</p>
      <p v-if="busy" role="status">Cargando…</p>
      <template v-if="venueData">
        <a v-if="menuPath" :href="menuPath" download>Descargá el menú (PDF)</a>
        <section v-if="venueData.cancellation_policy" class="gomar-feature-article">
          <h2>Política de cancelación</h2>
          <p class="gomar-feature-text">{{ venueData.cancellation_policy }}</p>
        </section>
        <form class="gomar-feature-filter" @submit.prevent="availability">
          <label>Fecha<input v-model="date" type="date" :min="todayInBuenosAires()" :disabled="busy" required @change="resetSlots" /></label>
          <label>Personas<input v-model.number="size" type="number" min="1" max="12" :disabled="busy" required @change="resetSlots" /></label>
          <button class="primary" :disabled="busy">Consultar horarios</button>
        </form>
        <p v-if="searched && !slots.length">No hay lugares para esa fecha y cantidad de personas.</p>
        <form v-if="slots.length" class="stack" @submit.prevent="book">
          <label>Horario
            <select v-model="slot" required :disabled="busy">
              <option value="" disabled>Elegí un horario</option>
              <option v-for="start in slots" :key="start" :value="start">{{ localTime(start) }}</option>
            </select>
          </label>
          <label>Nombre<input v-model="guest" autocomplete="name" maxlength="100" required :disabled="busy" /></label>
          <button class="primary" :disabled="busy || !slot">Solicitar reserva</button>
        </form>
      </template>
    </section>
  </main>
</template>
