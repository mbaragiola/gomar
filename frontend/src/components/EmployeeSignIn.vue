<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ApiError, request } from '../api'

const emit = defineEmits<{ cancel: []; authenticated: [] }>()
const organization = ref('')
const username = ref('')
const password = ref('')
const repeated = ref('')
const changing = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
let attempt = 0
function clear() {
  attempt++
  password.value = ''
  repeated.value = ''
  changing.value = false
  busy.value = false
}
onMounted(() => window.addEventListener('pagehide', clear))
onBeforeUnmount(() => {
  window.removeEventListener('pagehide', clear)
  clear()
})
async function submit() {
  if (busy.value) return
  const replacing = changing.value
  if (replacing && password.value !== repeated.value) {
    error.value = 'Las contraseñas no coinciden.'
    return
  }
  const org = Number(organization.value)
  if (!replacing && (!/^\d+$/.test(organization.value) || !Number.isSafeInteger(org) || org <= 0)) {
    error.value = 'Ingresá el número de organización que te dieron.'
    return
  }
  const body = replacing ? { password: password.value } : { organization: org, username: username.value, password: password.value }
  password.value = ''
  repeated.value = ''
  error.value = ''
  notice.value = ''
  busy.value = true
  const generation = ++attempt
  try {
    const result = await request<{ status: string }>('/auth/employee/' + (replacing ? 'password' : 'login'), {
      method: 'POST', cache: 'no-store', body: JSON.stringify(body),
    })
    if (generation !== attempt) return
    if (!replacing && result.status === 'change_password') changing.value = true
    else if (replacing && result.status === 'password_changed') {
      changing.value = false
      notice.value = 'Guardaste la contraseña. Ingresá con tu nueva contraseña.'
    } else if (!replacing && result.status === 'authenticated') emit('authenticated')
    else error.value = 'No pude comprobar el resultado.'
  } catch (cause) {
    if (generation !== attempt) return
    if (replacing && cause instanceof ApiError && cause.status === 401) {
      changing.value = false
      error.value = 'El permiso para cambiar la contraseña venció. Pedile una nueva contraseña temporal a tu administrador.'
    } else error.value = cause instanceof ApiError ? cause.message : 'No pude completar el ingreso.'
  } finally {
    if (generation === attempt) busy.value = false
  }
}
</script>

<template>
  <form class="stack" aria-label="Ingreso de empleados" @submit.prevent="submit">
    <h2>{{ changing ? 'Elegí tu nueva contraseña' : 'Ingresá como empleado' }}</h2>
    <template v-if="changing">
      <p>Por ahora solo podés cambiar la contraseña. Después vas a ingresar con la nueva.</p>
      <label>Nueva contraseña<input v-model="password" type="password" autocomplete="new-password" minlength="12" maxlength="256" required /></label>
      <label>Repetí la contraseña<input v-model="repeated" type="password" autocomplete="new-password" minlength="12" maxlength="256" required /></label>
    </template>
    <template v-else>
      <label>Número de organización<input v-model="organization" inputmode="numeric" autocomplete="off" required /></label>
      <label>Usuario de empleado<input v-model="username" autocomplete="username" maxlength="48" required /></label>
      <label>Contraseña de empleado<input v-model="password" type="password" autocomplete="current-password" maxlength="256" required /></label>
    </template>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <button class="primary" :disabled="busy">{{ changing ? 'Guardar nueva contraseña' : 'Ingresar al trabajo' }}</button>
    <button type="button" class="secondary" :disabled="busy" @click="emit('cancel')">Volver a opciones de ingreso</button>
  </form>
</template>
