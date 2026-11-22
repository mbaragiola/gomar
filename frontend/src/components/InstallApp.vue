<script setup lang="ts">
import { inject } from 'vue'
import { APP_INSTALL } from '../appInstall'

const install = inject(APP_INSTALL)
</script>

<template>
  <button v-if="install?.available.value" type="button" class="text-button" @click="install.install">
    Instalar app
  </button>
</template>
