<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134). doc.get, then chunk.list one server page at a time.
// The document comes first: chunks load only after it, so one load reports to the bridge at a time.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { getDocumentIn, listChunks, type Chunk, type DocumentDetail, type Page } from '../api'
import { INTENT_CITATIONS_MAX } from '../bridge'
import { CiteActions, citationRefOf, toggleRef, type CitationRef } from '../cite'
import { msg, type MessageKey } from '../i18n'
import { bridge, bridgeOpen, loads } from '../page'
import { statusKey } from './docStatus'
import MarkdownBlock from './MarkdownBlock.vue'
import PagerBar from './PagerBar.vue'

const props = defineProps<{ kbId: string; knowledgeId: string; page: number }>()
const emit = defineEmits<{ back: []; page: [page: number] }>()

const loading = ref(true)
const doc = ref<DocumentDetail | null>(null)
const chunks = ref<Page<Chunk> | null>(null)
const error = ref<MessageKey | null>(null)
const selected = ref<CitationRef[]>([])
const tick = ref(0)
const actions = new CiteActions(bridge, () => { tick.value++ })
onBeforeUnmount(() => actions.dispose())
const outcome = computed(() => { void tick.value; return actions.outcome })
const intentPending = computed(() => { void tick.value; return actions.intentPending })

async function load(kbId: string, knowledgeId: string, page: number) {
  selected.value = []
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
  ([kbId, id, page]) => {
    selected.value = []
    void load(kbId, id, page)
  },
  { immediate: true },
)

function refOf(c: Chunk) {
  return doc.value ? citationRefOf(doc.value, c) : null
}

function isSelected(id: string) {
  return selected.value.some((r) => r.chunk_id === id)
}

function onCite(e: MouseEvent, r: CitationRef) {
  actions.cite(r, e)
}

function onToggle(r: CitationRef) {
  selected.value = toggleRef(selected.value, r, INTENT_CITATIONS_MAX)
}

function onAsk(e: MouseEvent) {
  actions.returnToChat(selected.value, e)
  if (actions.intentPending) selected.value = []
}

function onCancel() {
  actions.cancelIntent()
}
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
      <div v-if="bridgeOpen && chunks.items.some((c) => refOf(c) !== null)" class="ea-cite-bar">
        <span class="ea-muted">{{ msg('cite.selected', { n: selected.length, max: INTENT_CITATIONS_MAX }) }}</span>
        <button type="button" :disabled="selected.length === 0 || intentPending" @click="onAsk">{{ msg('intent.button') }}</button>
        <template v-if="intentPending">
          <span>{{ msg('intent.waiting') }}</span>
          <button type="button" @click="onCancel">{{ msg('intent.cancel') }}</button>
        </template>
        <p v-if="outcome?.kind === 'sent'" class="ea-cite-result" role="status">{{ msg('cite.sent') }}</p>
        <p v-else-if="outcome?.kind === 'failed'" class="ea-cite-result" role="status">{{ msg('cite.failed', { c: outcome.code }) }}</p>
        <p v-else-if="outcome?.kind === 'not_sent'" class="ea-cite-result" role="status">{{ msg('cite.not_sent') }}</p>
      </div>
      <p v-if="chunks.items.length === 0" class="ea-muted">{{ msg('chunks.empty') }}</p>
      <ol v-else class="ea-chunks">
        <li v-for="c in chunks.items" :key="c.chunk_id" class="ea-chunk">
          <span class="ea-muted ea-chunk-pos">{{ msg('chunks.position', { n: c.position }) }}</span>
          <template v-if="bridgeOpen">
            <template v-for="r in [refOf(c)]" :key="c.chunk_id">
              <template v-if="r">
                <input
                  type="checkbox"
                  class="ea-cite-pick"
                  :checked="isSelected(c.chunk_id)"
                  :disabled="!isSelected(c.chunk_id) && selected.length >= INTENT_CITATIONS_MAX"
                  :aria-label="msg('cite.select', { n: c.position })"
                  @change="onToggle(r)"
                >
                <button type="button" class="ea-cite" @click="(e) => onCite(e, r)">{{ msg('cite.button') }}</button>
              </template>
            </template>
          </template>
          <MarkdownBlock :text="c.text" />
        </li>
      </ol>
      <PagerBar :page="chunks.page" :page-size="chunks.pageSize" :total="chunks.total" @go="(p) => emit('page', p)" />
    </template>
  </div>
</template>
