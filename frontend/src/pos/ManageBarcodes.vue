<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { ApiError, request } from '../api'
import { isBarcode, normalizedProducts, type Product } from './model'

const props = defineProps<{ entity: number; outlet: number; outletName: string }>()
const endpoint = `/entities/${props.entity}/outlets/${props.outlet}/pos/catalog`
const products = ref<Product[]>([])
const choice = ref(0)
const barcode = ref('')
const busy = ref(false)
const error = ref('')
const notice = ref('')
const lifetime = new AbortController()

function selectProduct() {
  barcode.value = products.value.find((p) => p.id === choice.value)?.codigo_barras ?? ''
  error.value = ''
  notice.value = ''
}

async function load() {
  busy.value = true
  try {
    const catalog = await request<{ productos: Product[] }>(endpoint, { cache: 'no-store', signal: lifetime.signal })
    normalizedProducts(catalog.productos) // Validate all rows, including inactive products.
    products.value = catalog.productos
    selectProduct()
  } catch {
    if (!lifetime.signal.aborted) error.value = 'No se pudo cargar el catálogo de este local.'
  } finally {
    busy.value = false
  }
}

async function save() {
  const product = products.value.find((p) => p.id === choice.value)
  const code = barcode.value
  if (!product || (code !== '' && !isBarcode(code)) || busy.value || code === (product.codigo_barras ?? '')) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await request<Product>(`${endpoint}/${product.id}/barcode`, {
      method: 'PUT', cache: 'no-store', signal: lifetime.signal,
      body: JSON.stringify({ codigo_barras: code, revision: product.revision }),
    })
    normalizedProducts([result])
    if (result.id !== product.id || result.revision !== product.revision + 1 ||
      (result.codigo_barras ?? '') !== code) throw new Error('unexpected catalog acknowledgment')
    products.value = products.value.map((p) => p.id === result.id ? result : p)
    notice.value = code ? 'Código de barras guardado.' : 'Código de barras quitado.'
  } catch (cause) {
    if (!lifetime.signal.aborted) error.value = cause instanceof ApiError ? cause.message : 'No se pudo verificar el cambio. Actualizá el catálogo.'
  } finally {
    busy.value = false
  }
}

onMounted(load)
onUnmounted(() => lifetime.abort())
</script>

<template>
  <section class="card gomar-feature" aria-label="Códigos de barras del catálogo">
    <h2>Códigos de barras del catálogo</h2>
    <p>Local: {{ outletName }}. Los cambios se ven al volver a habilitar una caja.</p>
    <form class="gomar-feature-actions" @submit.prevent="save">
      <label>Producto para asignar código
        <select v-model="choice" :disabled="busy" @change="selectProduct">
          <option :value="0">Elegí un producto</option>
          <option v-for="p in products" :key="p.id" :value="p.id">{{ p.nombre }}{{ p.activo ? '' : ' (inactivo)' }}</option>
        </select>
      </label>
      <label>Código de barras del producto
        <input v-model="barcode" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" :disabled="busy || !choice" />
      </label>
      <button type="submit" :disabled="busy || !choice || (!!barcode && !isBarcode(barcode)) || barcode === (products.find((p) => p.id === choice)?.codigo_barras ?? '')">Guardar código de barras</button>
    </form>
    <p>Usá hasta 80 caracteres visibles, sin espacios. Dejá el campo vacío para quitar el código.</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <button v-if="error" type="button" :disabled="busy" @click="load">Actualizar catálogo</button>
  </section>
</template>
