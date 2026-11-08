<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134). Previous / next over a server page.
import { computed } from 'vue'
import { msg } from '../i18n'

const props = defineProps<{ page: number; pageSize: number; total: number }>()
const emit = defineEmits<{ go: [page: number] }>()

const pages = computed(() => Math.max(1, Math.ceil(props.total / Math.max(1, props.pageSize))))
</script>

<template>
  <nav v-if="pages > 1 || page > 1" class="ea-pager">
    <button type="button" :disabled="page <= 1" @click="emit('go', page - 1)">{{ msg('pager.prev') }}</button>
    <span v-if="page <= pages" class="ea-muted">{{ msg('pager.of', { p: page, n: pages }) }}</span>
    <button type="button" :disabled="page >= pages" @click="emit('go', page + 1)">{{ msg('pager.next') }}</button>
  </nav>
</template>
