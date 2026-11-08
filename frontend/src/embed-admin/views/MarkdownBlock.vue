<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134, design §4). The only place the page sets HTML.
// renderMarkdown already escapes everything and emits only SAFE_TAGS; DOMPurify with the same allowlist is a second
// layer, a private instance so no global hook or config is shared with anything else.
import DOMPurify from 'dompurify'
import { computed } from 'vue'
import { msg } from '../i18n'
import { renderMarkdown, SAFE_ATTRS, SAFE_TAGS } from '../markdown'

const props = defineProps<{ text: string; wikiLinks?: ReadonlySet<string> }>()
const emit = defineEmits<{ wiki: [slug: string] }>()

const purify = DOMPurify(window)
purify.addHook('uponSanitizeAttribute', (node, data) => {
  if (data.attrName === 'class' && !/^ea-md-[a-z]+$/.test(data.attrValue)) data.keepAttr = false
  if (data.attrName === 'start' && (node.nodeName !== 'OL' || !/^\d{1,9}$/.test(data.attrValue))) data.keepAttr = false
  if (data.attrName === 'data-slug' && (node.nodeName !== 'SPAN' || node.getAttribute('class') !== 'ea-md-wiki')) {
    data.keepAttr = false
  }
})

const html = computed(() =>
  purify.sanitize(renderMarkdown(props.text, { image: msg('md.image') }, { wikiLinks: props.wikiLinks }), {
    ALLOWED_TAGS: [...SAFE_TAGS],
    ALLOWED_ATTR: [...SAFE_ATTRS],
    ALLOW_DATA_ATTR: false,
    ALLOW_ARIA_ATTR: false,
  }),
)

/** A click on an in-knowledge-base wiki link. The slug is read back from the sanitized DOM, never evaluated. */
function onClick(e: MouseEvent) {
  const el = e.target instanceof Element ? e.target.closest('span.ea-md-wiki') : null
  const slug = el?.getAttribute('data-slug')
  if (slug && props.wikiLinks?.has(slug)) emit('wiki', slug)
}
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -->
  <div class="ea-md" v-html="html" @click="onClick" />
</template>
