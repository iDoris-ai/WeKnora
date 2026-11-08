<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134). tag.list of one knowledge base.
import { ref, watch } from 'vue'
import { listTags, type Page, type Tag } from '../api'
import { msg, type MessageKey } from '../i18n'
import { loads } from '../page'
import { emptyKind, safeColor } from './docStatus'
import PagerBar from './PagerBar.vue'

const props = defineProps<{ kbId: string; page: number }>()
const emit = defineEmits<{ page: [page: number] }>()

const loading = ref(true)
const data = ref<Page<Tag> | null>(null)
const error = ref<MessageKey | null>(null)

async function load(kbId: string, page: number) {
  loading.value = true
  data.value = null
  error.value = null
  const run = loads.begin()
  try {
    const got = await listTags(kbId, page)
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
    <p v-else-if="data && emptyKind(data) === 'none'" class="ea-muted">{{ msg('tags.empty') }}</p>
    <template v-else-if="data">
      <ul class="ea-tags">
        <li v-for="t in data.items" :key="t.id" class="ea-tag-item">
          <span class="ea-swatch" :style="safeColor(t.color) ? { background: safeColor(t.color) } : undefined" />
          <span>{{ t.name }}</span>
          <span class="ea-muted">{{ msg('tags.documents', { n: t.document_count }) }}</span>
        </li>
      </ul>
      <p v-if="emptyKind(data) === 'beyond'" class="ea-muted">{{ msg('pager.beyond') }}</p>
      <PagerBar :page="data.page" :page-size="data.pageSize" :total="data.total" @go="(p) => emit('page', p)" />
    </template>
  </div>
</template>
