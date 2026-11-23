<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { request } from '../api'

const props = defineProps<{ organization: number; outlets: { id: number; nombre: string }[] }>()
const roles = { purchasing: 'Compras', accounting: 'Contabilidad', read_only: 'Consulta' } as const
const labels = { invited: 'Invitado', changing_password: 'Cambio pendiente', active: 'Activo', disabled: 'Desactivado' }
type Employee = { id: number; username: string; role: keyof typeof roles; status: keyof typeof labels }
type Invite = { id: number; organization: number; username: string; temporary_password: string; expires_at: string }
const employees = ref<Employee[]>([])
const truncated = ref(false)
const invite = ref<Invite | null>(null)
const visible = ref(false)
const username = ref('')
const role = ref<keyof typeof roles>('read_only')
const selected = ref<number[]>([])
const confirming = ref<{ employee: Employee; action: 'reset' | 'disable' } | null>(null)
const busy = ref(false)
const error = ref('')
const lifetime = new AbortController()
const path = '/entities/' + props.organization + '/employees'
let generation = 0
let revealTimer: ReturnType<typeof setTimeout> | undefined
function hide() {
  visible.value = false
  invite.value = null
  if (revealTimer !== undefined) clearTimeout(revealTimer)
}
function leave() {
  generation++
  busy.value = false
  confirming.value = null
  hide()
}
function reveal() {
  visible.value = true
  revealTimer = setTimeout(hide, 60000)
}
async function load(run: number) {
  const result = await request<{ employees: Employee[]; truncated: boolean }>(path, { cache: 'no-store', signal: lifetime.signal })
  if (run === generation) {
    employees.value = result.employees
    truncated.value = result.truncated
  }
}
function valid(receipt: Invite) {
  return receipt.organization === props.organization && Number.isSafeInteger(receipt.id) && receipt.id > 0 &&
    /^[a-z2-7]{32}$/.test(receipt.temporary_password) && Number.isFinite(Date.parse(receipt.expires_at))
}
async function run(endpoint?: string, data?: unknown) {
  if (busy.value) return
  hide()
  busy.value = true
  error.value = ''
  const current = ++generation
  try {
    if (endpoint && data) {
      const result = await request<Invite>(endpoint, { method: 'POST', body: JSON.stringify(data), cache: 'no-store', signal: lifetime.signal })
      if (current !== generation) return
      if (!endpoint.endsWith('/disable')) {
        if (!valid(result)) throw new Error('invalid invite receipt')
        invite.value = result
      }
      confirming.value = null
      if (endpoint === path) {
        username.value = ''
        selected.value = []
      }
    }
    await load(current)
  } catch {
    if (current === generation) error.value = 'No pude comprobar el resultado. Actualizá la lista antes de volver a intentar.'
  } finally {
    if (current === generation) busy.value = false
  }
}
onMounted(() => {
  window.addEventListener('pagehide', leave)
  void run()
})
onBeforeUnmount(() => {
  window.removeEventListener('pagehide', leave)
  lifetime.abort()
  leave()
})
</script>

<template>
  <section class="management-section stack" aria-label="Empleados de la organización">
    <h3>Empleados de la organización</h3>
    <form class="stack" @submit.prevent="run(path, { username, role, outlet_ids: selected })">
      <label>Usuario de empleado<input v-model="username" autocomplete="off" maxlength="48" required /></label>
      <label>Rol de empleado<select v-model="role"><option v-for="(label, value) in roles" :key="value" :value="value">{{ label }}</option></select></label>
      <fieldset>
        <legend>Locales del empleado</legend>
        <label v-for="outlet in outlets" :key="outlet.id" class="check"><input v-model="selected" type="checkbox" :value="outlet.id" /> {{ outlet.nombre }}</label>
      </fieldset>
      <button class="primary" :disabled="busy || !selected.length">Asignar acceso de empleado</button>
    </form>
    <section v-if="invite" class="card stack" aria-label="Contraseña temporal del empleado">
      <p>Compartí estos datos en privado. La contraseña se usa una vez. Si la ocultás, no podés volver a verla.</p>
      <p>Organización {{ invite.organization }} · Usuario {{ invite.username }}.</p>
      <p>Vence el {{ new Date(invite.expires_at).toLocaleString('es-AR', { timeZone: 'America/Argentina/Buenos_Aires' }) }} (hora argentina).</p>
      <button v-if="!visible" class="secondary" type="button" @click="reveal">Mostrar contraseña temporal</button>
      <template v-else>
        <p><code>{{ invite.temporary_password }}</code></p>
        <button class="secondary" type="button" @click="hide">Ocultar y borrar contraseña</button>
      </template>
    </section>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <button v-if="error || truncated" type="button" class="secondary" :disabled="busy" @click="run()">Actualizar empleados</button>
    <p v-if="truncated">Se muestran los últimos 100 empleados de tu alcance.</p>
    <ul v-if="employees.length" class="gomar-feature-list">
      <li v-for="employee in employees" :key="employee.id">
        <strong>{{ employee.username }}</strong> · {{ roles[employee.role] }} · {{ labels[employee.status] }}
        <template v-if="employee.status !== 'disabled'">
          <button type="button" class="secondary" :disabled="busy" @click="hide(); confirming = { employee, action: 'reset' }">Reemitir contraseña de {{ employee.username }}</button>
          <button type="button" class="secondary" :disabled="busy" @click="hide(); confirming = { employee, action: 'disable' }">Desactivar a {{ employee.username }}</button>
        </template>
      </li>
    </ul>
    <section v-if="confirming" class="stack" aria-label="Confirmar cambio de acceso">
      <p>{{ confirming.action === 'reset' ? 'Esto cierra sus sesiones y reemplaza la contraseña anterior.' : 'Esto cierra sus sesiones y le impide ingresar.' }} Usuario: {{ confirming.employee.username }}.</p>
      <button type="button" class="secondary" :disabled="busy" @click="run(path + '/' + confirming.employee.id + '/' + confirming.action, {})">Confirmar {{ confirming.action === 'reset' ? 'reemplazo de contraseña' : 'desactivación' }}</button>
      <button type="button" class="secondary" :disabled="busy" @click="confirming = null">Volver sin cambios</button>
    </section>
  </section>
</template>
