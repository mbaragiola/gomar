<script setup lang="ts">
import { inject } from 'vue'
import { APP_THEME } from '../theme'

const theme = inject(APP_THEME)
function select(event: Event) {
  if (!(event.target instanceof HTMLSelectElement)) return
  const { value } = event.target
  if (value === 'system' || value === 'light' || value === 'dark') theme?.set(value)
}
</script>

<template>
  <label v-if="theme" class="theme-control">
    <span>Apariencia</span>
    <select aria-label="Apariencia" :value="theme.mode.value" @change="select">
      <option value="system">Sistema</option>
      <option value="light">Claro</option>
      <option value="dark">Oscuro</option>
    </select>
  </label>
</template>
