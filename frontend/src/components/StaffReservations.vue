<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ApiError, request } from '../api'
import { reservationSlotUTC, reservationTimes } from '../reservation-format'
import { localDateTime, todayInBuenosAires } from '../format'

type Status = 'pending' | 'confirmed' | 'cancelled'
interface Booking {
  id: string
  guest_name: string
  party_size: number
  start_at: string
  status: Status
  table_label: string
  cancellation_policy: string | null
}
interface Menu {
  disponible: boolean
  nombre?: string
}
interface Settings {
  venue: string
  display_name: string
  cancellation_policy: string
  enabled: boolean
}

const props = defineProps<{ entity: number; outlet: number; canEdit: boolean }>()
const path = computed(() => `/entities/${props.entity}/outlets/${props.outlet}/reservations`)
const date = ref(todayInBuenosAires())
const rows = ref<Booking[]>([])
const truncated = ref(false)
const busy = ref(false)
const problem = ref('')
const notice = ref('')
const showSettings = ref(false)
const configured = ref(false)
const table = reactive({ label: '', capacity: 4 })
const slot = reactive({ day: todayInBuenosAires(), time: '' })
const menu = ref<Menu | null>(null)
const menuFile = ref<File | null>(null)
const menuInputKey = ref(0)
const menuIntent = ref<'write' | 'remove' | null>(null)
const cancellation = ref<Booking | null>(null)
const settings = reactive<Settings>({ venue: '', display_name: '', cancellation_policy: '', enabled: false })
const statuses: Record<Status, string> = { pending: 'Pendiente', confirmed: 'Confirmada', cancelled: 'Cancelada' }

function showError(err: unknown, fallback: string) {
  problem.value = err instanceof ApiError ? err.message : fallback
}

async function load() {
  if (busy.value) return
  busy.value = true
  problem.value = ''
  rows.value = []
  truncated.value = false
  cancellation.value = null
  try {
    const query = new URLSearchParams({ date: date.value })
    const result = await request<{ requests: Booking[]; truncated: boolean }>(`${path.value}?${query}`)
    rows.value = result.requests
    truncated.value = result.truncated
  } catch (err) {
    showError(err, 'No se pudieron cargar las reservas.')
  } finally {
    busy.value = false
  }
}

async function action(row: Booking, type: 'confirm' | 'cancel') {
  if (!props.canEdit || busy.value) return
  busy.value = true
  problem.value = ''
  notice.value = ''
  let success = false
  try {
    await request(`${path.value}/${encodeURIComponent(row.id)}/${type}`, { method: 'POST' })
    notice.value = type === 'confirm' ? 'Reserva confirmada.' : 'Reserva cancelada.'
    success = true
  } catch (err) {
    showError(err, 'No se pudo actualizar la reserva.')
  } finally {
    busy.value = false
  }
  if (success) await load()
}

function assignSettings(value: Settings) {
  Object.assign(settings, {
    venue: value.venue, display_name: value.display_name, cancellation_policy: value.cancellation_policy, enabled: value.enabled,
  })
}

async function openSettings() {
  if (!props.canEdit || busy.value) return
  if (showSettings.value) {
    showSettings.value = false
    return
  }
  busy.value = true
  problem.value = ''
  notice.value = ''
  try {
    assignSettings(await request<Settings>(`${path.value}/settings`))
    configured.value = true
    showSettings.value = true
    await loadMenu()
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      assignSettings({ venue: '', display_name: '', cancellation_policy: '', enabled: false })
      configured.value = false
      showSettings.value = true
    } else showError(err, 'No se pudo cargar la configuración.')
  } finally {
    busy.value = false
  }
}

async function saveSettings() {
  if (!props.canEdit || busy.value) return
  busy.value = true
  problem.value = ''
  notice.value = ''
  try {
    assignSettings(await request<Settings>(`${path.value}/settings`, {
      method: 'PUT', body: JSON.stringify(settings),
    }))
    configured.value = true
    notice.value = 'Configuración guardada.'
    await loadMenu()
  } catch (err) {
    showError(err, 'No se pudo guardar la configuración.')
  } finally {
    busy.value = false
  }
}

async function addTable() {
  if (!props.canEdit || !configured.value || busy.value || !table.label.trim()) return
  busy.value = true
  problem.value = ''
  notice.value = ''
  try {
    const result = await request<{ label: string; capacity: number }>(`${path.value}/tables`, {
      method: 'POST', body: JSON.stringify({ label: table.label.trim(), capacity: table.capacity }),
    })
    notice.value = `Mesa agregada: ${result.label}, hasta ${result.capacity} personas.`
    table.label = ''
  } catch (err) {
    showError(err, 'No se pudo agregar la mesa.')
  } finally {
    busy.value = false
  }
}

async function addSlot() {
  if (!props.canEdit || !configured.value || busy.value) return
  problem.value = ''
  notice.value = ''
  const startAt = reservationSlotUTC(slot.day, slot.time)
  if (!startAt) {
    problem.value = 'Elegí una fecha y un horario válidos.'
    return
  }
  busy.value = true
  try {
    const result = await request<{ start_at: string }>(`${path.value}/slots`, {
      method: 'POST', body: JSON.stringify({ start_at: startAt }),
    })
    notice.value = `Turno agregado: ${localDateTime(result.start_at)}.`
    slot.time = ''
  } catch (err) {
    showError(err, 'No se pudo agregar el turno.')
  } finally {
    busy.value = false
  }
}

async function loadMenu() {
  menu.value = null
  try {
    menu.value = await request<Menu>(`${path.value}/menu`)
  } catch {
    // The rest of the settings are usable when menu support is not enabled.
  }
}

function pickMenu(event: Event) {
  menuIntent.value = null
  problem.value = ''
  const file = (event.target as HTMLInputElement).files?.[0]
  menuFile.value = null
  if (!file) return
  if (!/\.pdf$/i.test(file.name) || (file.type && file.type !== 'application/pdf')
      || file.size === 0 || file.size > 4 * 1024 * 1024) {
    menuInputKey.value++
    problem.value = 'Elegí un archivo PDF de hasta 4 MB.'
    return
  }
  menuFile.value = file
}

async function changeMenu() {
  if (!props.canEdit || !menu.value || !menuIntent.value || busy.value) return
  const upload = menuIntent.value === 'write'
  if (upload && !menuFile.value) return
  busy.value = true
  problem.value = ''
  notice.value = ''
  try {
    menu.value = await request<Menu>(`${path.value}/menu`, upload ? {
      method: 'PUT', body: menuFile.value, headers: { 'Content-Type': 'application/pdf' },
    } : { method: 'DELETE' })
    notice.value = upload ? 'Menú guardado.' : 'Menú retirado.'
    menuFile.value = null
    menuInputKey.value++
    menuIntent.value = null
  } catch (err) {
    showError(err, 'No se pudo actualizar el menú.')
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <section class="card gomar-feature" aria-label="Reservas del local">
    <div class="section-heading">
      <div><p class="eyebrow">SALÓN</p><h2>Reservas del local</h2></div>
      <button v-if="canEdit" type="button" :disabled="busy" :aria-expanded="showSettings" @click="openSettings">
        {{ showSettings ? 'Cerrar configuración' : 'Configurar reservas' }}
      </button>
    </div>
    <p v-if="busy" role="status">Cargando…</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="problem" class="error" role="alert">{{ problem }}</p>
    <form v-if="showSettings" class="stack gomar-feature-article" @submit.prevent="saveSettings">
      <label>Identificador público<input v-model="settings.venue" pattern="[a-z0-9]+(-[a-z0-9]+)*" maxlength="48" required /></label>
      <label>Nombre que se muestra<input v-model="settings.display_name" maxlength="80" required /></label>
      <label>Política de cancelación<input v-model="settings.cancellation_policy" maxlength="500" /></label>
      <label class="gomar-feature-check"><input v-model="settings.enabled" type="checkbox" /> Aceptar solicitudes de reserva</label>
      <button class="primary" :disabled="busy">Guardar configuración</button>
    </form>
    <section v-if="showSettings && configured" class="gomar-feature-article gomar-feature">
      <div>
        <h3>Mesas y turnos</h3>
        <p class="muted">Agregá una mesa y un turno para ofrecer horarios. Cada turno dura 90 minutos.</p>
      </div>
      <form class="gomar-feature-filter" @submit.prevent="addTable">
        <label>Nombre de la mesa<input v-model="table.label" maxlength="32" required :disabled="busy" /></label>
        <label>Capacidad<input v-model.number="table.capacity" type="number" min="1" max="12" required :disabled="busy" /></label>
        <button :disabled="busy">Agregar mesa</button>
      </form>
      <form class="gomar-feature-filter" @submit.prevent="addSlot">
        <label>Fecha del turno<input v-model="slot.day" type="date" :min="todayInBuenosAires()" required :disabled="busy" /></label>
        <label>Hora del turno
          <select v-model="slot.time" required :disabled="busy">
            <option value="" disabled>Elegí una hora</option>
            <option v-for="time in reservationTimes" :key="time" :value="time">{{ time }}</option>
          </select>
        </label>
        <button :disabled="busy">Agregar turno</button>
      </form>
    </section>
    <section v-if="showSettings && configured && menu" class="gomar-feature-article gomar-feature">
      <div>
        <h3>Menú público (PDF)</h3>
        <p>El archivo será público cuando las reservas del local estén habilitadas. No subas archivos con datos privados.</p>
        <p>{{ menu.disponible ? 'Hay un menú guardado.' : 'No hay un menú guardado.' }}</p>
      </div>
      <label>Archivo PDF<input :key="menuInputKey" type="file" accept=".pdf,application/pdf" :disabled="busy" @change="pickMenu" /></label>
      <div class="gomar-feature-actions">
        <button type="button" :disabled="busy || !menuFile" @click="menuIntent = 'write'">
          {{ menu.disponible ? 'Reemplazar menú PDF' : 'Subir menú PDF' }}
        </button>
        <button v-if="menu.disponible" type="button" :disabled="busy" @click="menuIntent = 'remove'">Retirar menú PDF</button>
      </div>
      <div v-if="menuIntent" role="group" aria-label="Confirmar cambio de menú">
        <p>{{ menuIntent === 'write' ? '¿Confirmás que este PDF es para el público?' : '¿Querés retirar el PDF público?' }}</p>
        <div class="gomar-feature-actions">
          <button type="button" class="primary" :disabled="busy" @click="changeMenu">Confirmar cambio de menú</button>
          <button type="button" :disabled="busy" @click="menuIntent = null">Volver sin cambiar</button>
        </div>
      </div>
    </section>
    <form class="gomar-feature-filter" @submit.prevent="load">
      <label>Fecha<input v-model="date" type="date" required :disabled="busy" /></label>
      <button :disabled="busy">Ver reservas</button>
    </form>
    <p v-if="truncated" role="status">Hay más reservas de las que se pueden mostrar para esta fecha.</p>
    <p v-if="!rows.length && !problem && !busy">No hay reservas para esa fecha.</p>
    <ul v-else-if="rows.length" class="gomar-feature-list">
      <li v-for="row in rows" :key="row.id" class="gomar-feature-article">
        <strong>{{ row.guest_name }}</strong>
        <p>{{ localDateTime(row.start_at) }} · {{ row.party_size }} personas · {{ statuses[row.status] }}</p>
        <p v-if="row.table_label">Mesa: {{ row.table_label }}</p>
        <p v-if="row.cancellation_policy" class="gomar-feature-text">Política al solicitar: {{ row.cancellation_policy }}</p>
        <div v-if="canEdit && row.status !== 'cancelled'" class="gomar-feature-actions">
          <button v-if="row.status === 'pending'" type="button" :disabled="busy" @click="action(row, 'confirm')">Confirmar reserva</button>
          <button type="button" :disabled="busy" @click="cancellation = row">Cancelar reserva</button>
        </div>
      </li>
    </ul>
    <div v-if="cancellation" class="gomar-feature-article" role="group" aria-label="Confirmar cancelación">
      <p>¿Querés cancelar la reserva de {{ cancellation.guest_name }}?</p>
      <div class="gomar-feature-actions">
        <button type="button" class="primary" :disabled="busy" @click="action(cancellation, 'cancel')">Sí, cancelar reserva</button>
        <button type="button" :disabled="busy" @click="cancellation = null">No, volver</button>
      </div>
    </div>
  </section>
</template>
