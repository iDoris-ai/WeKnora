<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02c, WeKnora#134). wiki.list: published pages of one knowledge base.
import { ref, watch } from 'vue'
import { listWikiPages, type Page, type WikiListItem } from '../api'
import { formatDate, msg, type MessageKey } from '../i18n'
import { loads } from '../page'
import PagerBar from './PagerBar.vue'

const props = defineProps<{ kbId: string; page: number }>()
const emit = defineEmits<{ open: [slug: string]; page: [page: number] }>()

const loading = ref(true)
const data = ref<Page<WikiListItem> | null>(null)
const error = ref<MessageKey | null>(null)

async function load(kbId: string, page: number) {
  loading.value = true
  data.value = null
  error.value = null
  const run = loads.begin()
  try {
    const got = await listWikiPages(kbId, page)
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
    <p v-else-if="data && data.items.length === 0" class="ea-muted">{{ msg('wiki.empty') }}</p>
    <template v-else-if="data">
      <ul class="ea-rows">
        <li v-for="w in data.items" :key="w.slug">
          <button type="button" class="ea-row" @click="emit('open', w.slug)">
            <span class="ea-row-title">
              {{ w.title || w.slug }}
              <span v-if="w.summary" class="ea-muted ea-row-summary">{{ w.summary }}</span>
            </span>
            <span v-if="w.page_type" class="ea-tag">{{ w.page_type }}</span>
            <span class="ea-muted ea-row-meta">{{ msg('list.updated', { d: formatDate(w.updated_at) }) }}</span>
          </button>
        </li>
      </ul>
      <PagerBar :page="data.page" :page-size="data.pageSize" :total="data.total" @go="(p) => emit('page', p)" />
    </template>
  </div>
</template>
