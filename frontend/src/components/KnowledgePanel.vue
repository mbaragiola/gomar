<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ApiError, request } from '../api'
import { localDateTime } from '../format'

interface Article {
  id: number
  titulo: string
  contenido: string
  version: number
  creado_en: string
  actualizado_en: string
}

const props = defineProps<{ entity: number; outlet: number; canEdit: boolean }>()
const path = computed(() => `/entities/${props.entity}/outlets/${props.outlet}/knowledge`)
const search = ref('')
const results = ref<Article[]>([])
const selected = ref<Article | null>(null)
const edit = ref(false)
const title = ref('')
const body = ref('')
const busy = ref(false)
const problem = ref('')
const notice = ref('')

function showError(err: unknown, fallback: string) {
  problem.value = err instanceof ApiError ? err.message : fallback
}

async function load() {
  if (busy.value) return
  busy.value = true
  problem.value = ''
  try {
    const params = new URLSearchParams()
    if (search.value.trim()) params.set('q', search.value.trim())
    results.value = (await request<{ articulos: Article[] }>(`${path.value}?${params}`)).articulos
  } catch (err) {
    results.value = []
    showError(err, 'No se pudo cargar el manual.')
  } finally {
    busy.value = false
  }
}

async function select(id: number) {
  if (busy.value) return
  busy.value = true
  problem.value = ''
  notice.value = ''
  try {
    selected.value = await request<Article>(`${path.value}/${id}`)
    edit.value = false
  } catch (err) {
    showError(err, 'No se pudo abrir la página.')
  } finally {
    busy.value = false
  }
}

function begin(article: Article | null) {
  if (!props.canEdit || busy.value) return
  selected.value = article
  title.value = article?.titulo ?? ''
  body.value = article?.contenido ?? ''
  problem.value = ''
  notice.value = ''
  edit.value = true
}

async function save() {
  if (!props.canEdit || busy.value || !title.value.trim() || !body.value.trim()) return
  busy.value = true
  problem.value = ''
  const article = selected.value
  const values = { titulo: title.value.trim(), contenido: body.value }
  try {
    selected.value = await request<Article>(article ? `${path.value}/${article.id}` : path.value, {
      method: article ? 'PUT' : 'POST',
      body: JSON.stringify(article ? { ...values, version: article.version } : values),
    })
    edit.value = false
    notice.value = 'Página guardada.'
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      problem.value = 'Alguien actualizó la página. Copiá tus cambios antes de volver a abrirla.'
    } else showError(err, 'No se pudo guardar la página.')
  } finally {
    busy.value = false
  }
  if (!edit.value) await load()
}

onMounted(load)
</script>

<template>
  <section class="card gomar-feature" aria-label="Manual del local">
    <div class="section-heading">
      <div><p class="eyebrow">EQUIPO</p><h2>Manual del local</h2></div>
      <button v-if="canEdit" type="button" :disabled="busy" @click="begin(null)">Nueva página</button>
    </div>
    <form class="gomar-feature-filter" @submit.prevent="load">
      <label>Buscar en el manual<input v-model="search" type="search" maxlength="120" /></label>
      <button :disabled="busy">Buscar</button>
    </form>
    <p v-if="busy" role="status">Cargando…</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="problem" class="error" role="alert">{{ problem }}</p>
    <ul v-if="results.length" class="gomar-feature-list">
      <li v-for="item in results" :key="item.id">
        <button type="button" :disabled="busy" @click="select(item.id)">{{ item.titulo }}</button>
      </li>
    </ul>
    <p v-else-if="!busy && !problem">No hay páginas para mostrar.</p>
    <form v-if="edit" class="stack" @submit.prevent="save">
      <label>Título<input v-model="title" maxlength="120" required /></label>
      <label>Contenido<textarea v-model="body" rows="7" maxlength="12000" required /></label>
      <div class="gomar-feature-actions">
        <button class="primary" :disabled="busy">Guardar página</button>
        <button type="button" :disabled="busy" @click="edit = false">Cancelar</button>
      </div>
    </form>
    <article v-else-if="selected" class="gomar-feature-article">
      <h3>{{ selected.titulo }}</h3>
      <p class="muted">Actualizada: {{ localDateTime(selected.actualizado_en) }}</p>
      <p class="gomar-feature-text">{{ selected.contenido }}</p>
      <button v-if="canEdit" type="button" :disabled="busy" @click="begin(selected)">Editar página</button>
    </article>
  </section>
</template>
