<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134). doc.get, then chunk.list one server page at a time.
// The document comes first: chunks load only after it, so one load reports to the bridge at a time.
import { ref, watch } from 'vue'
import { getDocumentIn, listChunks, type Chunk, type DocumentDetail, type Page } from '../api'
import { msg, type MessageKey } from '../i18n'
import { loads } from '../page'
import { statusKey } from './docStatus'
import MarkdownBlock from './MarkdownBlock.vue'
import PagerBar from './PagerBar.vue'

const props = defineProps<{ kbId: string; knowledgeId: string; page: number }>()
const emit = defineEmits<{ back: []; page: [page: number] }>()

const loading = ref(true)
const doc = ref<DocumentDetail | null>(null)
const chunks = ref<Page<Chunk> | null>(null)
const error = ref<MessageKey | null>(null)

async function load(kbId: string, knowledgeId: string, page: number) {
  loading.value = true
  doc.value = null
  chunks.value = null
  error.value = null
  const run = loads.begin()
  try {
    const d = await getDocumentIn(kbId, knowledgeId)
    if (!run.current) return
    const c = await listChunks(knowledgeId, page)
    if (!run.current) return
    doc.value = d
    chunks.value = c
    run.ready()
  } catch (err) {
    const key = run.error(err)
    if (run.current) error.value = key
  } finally {
    if (run.current) loading.value = false
  }
}

watch(
  () => [props.kbId, props.knowledgeId, props.page] as const,
  ([kbId, id, page]) => void load(kbId, id, page),
  { immediate: true },
)
</script>

<template>
  <div>
    <button type="button" class="ea-back" @click="emit('back')">← {{ msg('docs.back') }}</button>
    <p v-if="loading" class="ea-muted">{{ msg('state.loading') }}</p>
    <p v-else-if="error" class="ea-error" role="alert">{{ msg(error) }}</p>
    <template v-else-if="doc && chunks">
      <header class="ea-detail-head">
        <h2 class="ea-subtitle">{{ doc.title || doc.file_name }}</h2>
        <span class="ea-tag">{{ msg(statusKey(doc.parse_status)) }}</span>
      </header>
      <p class="ea-muted">
        <span v-if="doc.title && doc.file_name">{{ doc.file_name }} · </span>
        {{ msg('docs.chunks', { n: doc.chunk_count }) }} ·
        {{ msg('docs.revision', { r: doc.revision.source_revision.slice(0, 12) }) }}
      </p>
      <p v-if="chunks.items.length === 0" class="ea-muted">{{ msg('chunks.empty') }}</p>
      <ol v-else class="ea-chunks">
        <li v-for="c in chunks.items" :key="c.chunk_id" class="ea-chunk">
          <span class="ea-muted ea-chunk-pos">{{ msg('chunks.position', { n: c.position }) }}</span>
          <MarkdownBlock :text="c.text" />
        </li>
      </ol>
      <PagerBar :page="chunks.page" :page-size="chunks.pageSize" :total="chunks.total" @go="(p) => emit('page', p)" />
    </template>
  </div>
</template>
