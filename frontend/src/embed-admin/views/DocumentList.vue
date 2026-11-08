<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134). doc.list of one knowledge base, one server page at a time.
import { ref, watch } from 'vue'
import { listDocuments, type DocumentItem, type Page } from '../api'
import { formatDate, msg, type MessageKey } from '../i18n'
import { loads } from '../page'
import { emptyKind, statusKey } from './docStatus'
import PagerBar from './PagerBar.vue'

const props = defineProps<{ kbId: string; page: number }>()
const emit = defineEmits<{ open: [knowledgeId: string]; page: [page: number] }>()

const loading = ref(true)
const data = ref<Page<DocumentItem> | null>(null)
const error = ref<MessageKey | null>(null)

async function load(kbId: string, page: number) {
  loading.value = true
  data.value = null
  error.value = null
  const run = loads.begin()
  try {
    const got = await listDocuments(kbId, page)
    if (!run.current) return
    data.value = got
    run.ready()
  } catch (err) {
    const key = run.error(err)
    if (run.current) error.value = key
  } finally {
    if (run.current) loading.value = false
  }
}

watch(() => [props.kbId, props.page] as const, ([kbId, page]) => void load(kbId, page), { immediate: true })
</script>

<template>
  <div>
    <p v-if="loading" class="ea-muted">{{ msg('state.loading') }}</p>
    <p v-else-if="error" class="ea-error" role="alert">{{ msg(error) }}</p>
    <p v-else-if="data && emptyKind(data) === 'none'" class="ea-muted">{{ msg('docs.empty') }}</p>
    <template v-else-if="data">
      <ul class="ea-rows">
        <li v-for="d in data.items" :key="d.knowledge_id">
          <button type="button" class="ea-row" @click="emit('open', d.knowledge_id)">
            <span class="ea-row-title">{{ d.title || d.file_name }}</span>
            <span class="ea-tag">{{ msg(statusKey(d.parse_status)) }}</span>
            <span class="ea-muted ea-row-meta">{{ msg('list.updated', { d: formatDate(d.updated_at) }) }}</span>
          </button>
        </li>
      </ul>
      <p v-if="emptyKind(data) === 'beyond'" class="ea-muted">{{ msg('pager.beyond') }}</p>
      <PagerBar :page="data.page" :page-size="data.pageSize" :total="data.total" @go="(p) => emit('page', p)" />
    </template>
  </div>
</template>
