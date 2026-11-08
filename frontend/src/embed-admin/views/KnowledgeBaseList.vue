<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). kb.list: every knowledge base the credential can see, in E's order.
import { onMounted, ref } from 'vue'
import { listKnowledgeBases, type KnowledgeBase } from '../api'
import { formatDate, msg, type MessageKey } from '../i18n'
import { reportError, reportLoading, reportReady } from '../page'
import { typeKey } from './kbType'

const loading = ref(true)
const items = ref<KnowledgeBase[]>([])
const error = ref<MessageKey | null>(null)

onMounted(async () => {
  reportLoading()
  try {
    items.value = await listKnowledgeBases()
    reportReady()
  } catch (err) {
    error.value = reportError(err)
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <section class="ea-page">
    <h1 class="ea-title">{{ msg('title.list') }}</h1>
    <p v-if="loading" class="ea-muted">{{ msg('state.loading') }}</p>
    <p v-else-if="error" class="ea-error" role="alert">{{ msg(error) }}</p>
    <p v-else-if="items.length === 0" class="ea-muted">{{ msg('list.empty') }}</p>
    <ul v-else class="ea-cards">
      <li v-for="kb in items" :key="kb.id">
        <RouterLink class="ea-card" :to="{ name: 'kb-detail', params: { kbId: kb.id } }">
          <div class="ea-card-head">
            <span class="ea-card-name">{{ kb.name }}</span>
            <span class="ea-tag">{{ msg(typeKey(kb.type)) }}</span>
            <span v-if="kb.wiki_enabled" class="ea-tag">{{ msg('kb.wiki') }}</span>
          </div>
          <p v-if="kb.description" class="ea-card-desc">{{ kb.description }}</p>
          <p class="ea-muted ea-card-meta">
            {{ msg('list.documents', { n: kb.document_count }) }} ·
            {{ msg('list.updated', { d: formatDate(kb.updated_at) }) }}
          </p>
        </RouterLink>
      </li>
    </ul>
  </section>
</template>
