<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02a/b, WeKnora#134). kb.get, then one tab: documents (list or one document with
// its chunks) or tags. What is shown lives in the query (tab, doc, page): the server answers only
// /embed-admin/knowledge-bases[/:kb_id], so a reload must stay on that path. The tab loads after the knowledge
// base, so one load reports to the bridge at a time.
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getKnowledgeBase, type KnowledgeBase } from '../api'
import { formatDate, msg, type MessageKey } from '../i18n'
import { loads } from '../page'
import DocumentDetail from './DocumentDetail.vue'
import DocumentList from './DocumentList.vue'
import { pageParam } from './docStatus'
import { typeKey } from './kbType'
import TagList from './TagList.vue'

const route = useRoute()
const router = useRouter()
const loading = ref(true)
const kb = ref<KnowledgeBase | null>(null)
const error = ref<MessageKey | null>(null)

const kbId = computed(() => (typeof route.params.kbId === 'string' ? route.params.kbId : ''))
const tab = computed(() => (route.query.tab === 'tags' ? 'tags' : 'documents'))
const docId = computed(() => (typeof route.query.doc === 'string' && route.query.doc !== '' ? route.query.doc : ''))
const page = computed(() => pageParam(route.query.page))

function show(query: Record<string, string>) {
  void router.push({ name: 'kb-detail', params: { kbId: kbId.value }, query })
}

async function load(id: string) {
  loading.value = true
  kb.value = null
  error.value = null
  const run = loads.begin()
  try {
    const got = await getKnowledgeBase(id)
    if (!run.current) return
    kb.value = got
    run.ready()
  } catch (err) {
    const key = run.error(err)
    if (run.current) error.value = key
  } finally {
    if (run.current) loading.value = false
  }
}

watch(
  kbId,
  (id) => {
    if (id !== '') void load(id)
  },
  { immediate: true },
)
</script>

<template>
  <section class="ea-page">
    <RouterLink class="ea-back" :to="{ name: 'kb-list' }">← {{ msg('detail.back') }}</RouterLink>
    <p v-if="loading" class="ea-muted">{{ msg('state.loading') }}</p>
    <p v-else-if="error" class="ea-error" role="alert">{{ msg(error) }}</p>
    <template v-else-if="kb">
      <header class="ea-detail-head">
        <h1 class="ea-title">{{ kb.name }}</h1>
        <span class="ea-tag">{{ msg(typeKey(kb.type)) }}</span>
        <span v-if="kb.wiki_enabled" class="ea-tag">{{ msg('kb.wiki') }}</span>
      </header>
      <p v-if="kb.description" class="ea-card-desc">{{ kb.description }}</p>
      <p class="ea-muted">
        {{ msg('list.documents', { n: kb.document_count }) }} ·
        {{ msg('detail.created', { d: formatDate(kb.created_at) }) }} ·
        {{ msg('list.updated', { d: formatDate(kb.updated_at) }) }}
      </p>
      <nav class="ea-tabs" role="tablist">
        <button type="button" role="tab" :aria-selected="tab === 'documents'" @click="show({})">
          {{ msg('tab.documents') }}
        </button>
        <button type="button" role="tab" :aria-selected="tab === 'tags'" @click="show({ tab: 'tags' })">
          {{ msg('tab.tags') }}
        </button>
      </nav>
      <TagList v-if="tab === 'tags'" :kb-id="kb.id" :page="page" @page="(p) => show({ tab: 'tags', page: String(p) })" />
      <DocumentDetail
        v-else-if="docId"
        :knowledge-id="docId"
        :page="page"
        @back="show({})"
        @page="(p) => show({ doc: docId, page: String(p) })"
      />
      <DocumentList
        v-else
        :kb-id="kb.id"
        :page="page"
        @open="(id) => show({ doc: id })"
        @page="(p) => show({ page: String(p) })"
      />
    </template>
  </section>
</template>
