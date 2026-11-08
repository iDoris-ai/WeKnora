<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02c, WeKnora#134, design §9). wiki.get: one published page. Links move only inside
// this knowledge base: a [[slug]] in the content or an out_link opens that wiki page here, a backlink likewise, and a
// source document opens the document view. Nothing navigates the frame.
import { computed, ref, watch } from 'vue'
import { getWikiPage, type WikiPage } from '../api'
import { formatDate, msg, type MessageKey } from '../i18n'
import { loads } from '../page'
import MarkdownBlock from './MarkdownBlock.vue'

const props = defineProps<{ kbId: string; slug: string }>()
const emit = defineEmits<{ back: []; wiki: [slug: string]; doc: [knowledgeId: string] }>()

const loading = ref(true)
const page = ref<WikiPage | null>(null)
const error = ref<MessageKey | null>(null)
const links = computed(() => new Set(page.value?.out_links ?? []))

async function load(kbId: string, slug: string) {
  loading.value = true
  page.value = null
  error.value = null
  const run = loads.begin()
  try {
    const got = await getWikiPage(kbId, slug)
    if (!run.current) return
    page.value = got
    run.ready()
  } catch (err) {
    const key = run.error(err)
    if (run.current) error.value = key
  } finally {
    if (run.current) loading.value = false
  }
}

watch(() => [props.kbId, props.slug] as const, ([kbId, slug]) => void load(kbId, slug), { immediate: true })
</script>

<template>
  <div>
    <button type="button" class="ea-back" @click="emit('back')">← {{ msg('wiki.back') }}</button>
    <p v-if="loading" class="ea-muted">{{ msg('state.loading') }}</p>
    <p v-else-if="error" class="ea-error" role="alert">{{ msg(error) }}</p>
    <article v-else-if="page">
      <header class="ea-detail-head">
        <h2 class="ea-subtitle">{{ page.title || page.slug }}</h2>
        <span v-if="page.page_type" class="ea-tag">{{ page.page_type }}</span>
      </header>
      <p class="ea-muted">
        <span v-if="page.aliases.length">{{ msg('wiki.aliases', { a: page.aliases.join('、') }) }} · </span>
        {{ msg('wiki.version', { v: page.version }) }} ·
        {{ msg('list.updated', { d: formatDate(page.updated_at) }) }}
      </p>
      <p v-if="page.summary" class="ea-card-desc">{{ page.summary }}</p>
      <MarkdownBlock :text="page.content" :wiki-links="links" @wiki="(s) => emit('wiki', s)" />
      <section v-if="page.out_links.length" class="ea-wiki-rel">
        <h3>{{ msg('wiki.links') }}</h3>
        <ul>
          <li v-for="s in page.out_links" :key="s">
            <button type="button" class="ea-link" @click="emit('wiki', s)">{{ s }}</button>
          </li>
        </ul>
      </section>
      <section v-if="page.in_links.length" class="ea-wiki-rel">
        <h3>{{ msg('wiki.backlinks') }}</h3>
        <ul>
          <li v-for="l in page.in_links" :key="l.slug">
            <button type="button" class="ea-link" @click="emit('wiki', l.slug)">{{ l.title || l.slug }}</button>
          </li>
        </ul>
      </section>
      <section v-if="page.source_documents.length" class="ea-wiki-rel">
        <h3>{{ msg('wiki.sources') }}</h3>
        <ul>
          <li v-for="d in page.source_documents" :key="d.knowledge_id">
            <button type="button" class="ea-link" @click="emit('doc', d.knowledge_id)">{{ d.title || d.knowledge_id }}</button>
          </li>
        </ul>
      </section>
    </article>
  </div>
</template>
