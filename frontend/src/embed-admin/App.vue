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
.ea-subtitle {
  font-size: 16px;
  margin: 0;
}
.ea-tabs {
  display: flex;
  gap: 4px;
  margin: 16px 0 12px;
  border-bottom: 1px solid var(--ea-border);
}
.ea-tabs button,
.ea-pager button,
.ea-row,
button.ea-back {
  font: inherit;
  color: inherit;
  background: none;
  border: 0;
  cursor: pointer;
}
.ea-tabs button {
  padding: 6px 12px;
  border-bottom: 2px solid transparent;
}
.ea-tabs button[aria-selected='true'] {
  border-bottom-color: var(--ea-accent);
  color: var(--ea-accent);
}
.ea-rows,
.ea-tags,
.ea-chunks {
  list-style: none;
  margin: 0;
  padding: 0;
}
.ea-row {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 8px;
  padding: 8px 4px;
  border-bottom: 1px solid var(--ea-border);
  text-align: left;
}
.ea-row:hover,
.ea-row:focus-visible {
  color: var(--ea-accent);
}
.ea-row-title {
  flex: 1;
  overflow-wrap: anywhere;
}
.ea-row-meta {
  font-size: 12px;
}
.ea-pager {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 12px;
}
.ea-pager button:disabled {
  opacity: 0.4;
  cursor: default;
}
.ea-chunk {
  padding: 8px 0;
  border-bottom: 1px solid var(--ea-border);
}
.ea-chunk-pos {
  font-size: 12px;
}
.ea-cite-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
  margin: 8px 0;
}
.ea-cite,
.ea-cite-bar button {
  font: inherit;
  padding: 2px 8px;
  cursor: pointer;
}
.ea-cite-bar button:disabled {
  cursor: default;
  opacity: 0.5;
}
.ea-cite-pick {
  margin: 0 6px;
}
.ea-cite-result {
  margin: 4px 0;
}
.ea-md {
  overflow-wrap: anywhere;
}
.ea-md pre {
  overflow-x: auto;
  padding: 8px;
  background: var(--ea-card);
  border-radius: 4px;
}
.ea-md table {
  border-collapse: collapse;
}
.ea-md th,
.ea-md td {
  border: 1px solid var(--ea-border);
  padding: 2px 6px;
}
.ea-md blockquote {
  margin: 0;
  padding-left: 12px;
  border-left: 3px solid var(--ea-border);
  color: var(--ea-muted);
}
.ea-md-url,
.ea-md-img {
  color: var(--ea-muted);
}
.ea-row-summary {
  display: block;
  font-size: 12px;
}
.ea-md-wiki,
.ea-link {
  color: var(--ea-accent);
  cursor: pointer;
}
.ea-link {
  font: inherit;
  background: none;
  border: 0;
  padding: 0;
  text-align: left;
}
.ea-wiki-rel h3 {
  font-size: 14px;
  margin: 16px 0 4px;
}
.ea-wiki-rel ul {
  margin: 0;
  padding-left: 18px;
}
.ea-tag-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
}
.ea-swatch {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--ea-muted);
}
.ea-back {
  display: inline-block;
  margin-bottom: 12px;
  color: var(--ea-accent);
  text-decoration: none;
}
</style>
