<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). kb.get and the frame the next slices fill
// (documents and tags in b, wiki in c).
import { ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { getKnowledgeBase, type KnowledgeBase } from '../api'
import { formatDate, msg, type MessageKey } from '../i18n'
import { loads } from '../page'
import { typeKey } from './kbType'

const route = useRoute()
const loading = ref(true)
const kb = ref<KnowledgeBase | null>(null)
const error = ref<MessageKey | null>(null)

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
  () => route.params.kbId,
  (id) => {
    if (typeof id === 'string' && id !== '') void load(id)
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
    </template>
  </section>
</template>
