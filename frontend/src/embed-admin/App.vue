<script setup lang="ts">
// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). No menu, command palette, chat, assistant, login or memory.
</script>

<template>
  <main class="ea-root">
    <RouterView />
  </main>
</template>

<style>
:root {
  --ea-bg: #ffffff;
  --ea-fg: #1f2329;
  --ea-muted: #646a73;
  --ea-border: #dee0e3;
  --ea-card: #f7f8fa;
  --ea-accent: #0052d9;
  --ea-error: #d54941;
  color-scheme: light;
}
:root[data-theme='dark'] {
  --ea-bg: #18191b;
  --ea-fg: #e8e9eb;
  --ea-muted: #a1a5ab;
  --ea-border: #36383b;
  --ea-card: #232427;
  --ea-accent: #4d8cff;
  --ea-error: #f07068;
  color-scheme: dark;
}
html,
body {
  margin: 0;
  background: var(--ea-bg);
  color: var(--ea-fg);
  font: 14px/1.6 -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif;
}
.ea-root {
  max-width: 960px;
  margin: 0 auto;
  padding: 24px 16px;
}
.ea-title {
  font-size: 20px;
  margin: 0 0 16px;
}
.ea-muted {
  color: var(--ea-muted);
}
.ea-error {
  color: var(--ea-error);
}
.ea-cards {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 12px;
}
.ea-card {
  display: block;
  padding: 12px 16px;
  border: 1px solid var(--ea-border);
  border-radius: 8px;
  background: var(--ea-card);
  color: inherit;
  text-decoration: none;
}
.ea-card:hover,
.ea-card:focus-visible {
  border-color: var(--ea-accent);
}
.ea-card-head,
.ea-detail-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.ea-detail-head .ea-title {
  margin: 0;
}
.ea-card-name {
  font-weight: 600;
}
.ea-card-desc {
  margin: 4px 0;
  overflow-wrap: anywhere;
}
.ea-card-meta {
  margin: 4px 0 0;
  font-size: 12px;
}
.ea-tag {
  font-size: 12px;
  padding: 0 6px;
  border: 1px solid var(--ea-border);
  border-radius: 4px;
  color: var(--ea-muted);
}
.ea-back {
  display: inline-block;
  margin-bottom: 12px;
  color: var(--ea-accent);
  text-decoration: none;
}
</style>
