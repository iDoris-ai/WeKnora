// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134, design §4/§8). The embed knowledge page's own build:
//   npm run build:embed-admin   → dist-embed-admin/embed-admin.html + dist-embed-admin/assets/*
// The WeKnora app serves that directory (AGENT24_KNOWLEDGE_EMBED_ADMIN_DIR) at /embed-admin/ in integration mode.
// Nothing is inlined (no data: URIs), the public/ directory is not copied, and the module preload polyfill
// goes into the entry chunk, never into the HTML: the page CSP allows no inline script.

import { fileURLToPath, URL } from 'node:url'
import { resolve, dirname } from 'node:path'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const __dirname = dirname(fileURLToPath(import.meta.url))

export default defineConfig({
  base: '/embed-admin/',
  publicDir: false,
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: 'dist-embed-admin',
    emptyOutDir: true,
    assetsDir: 'assets',
    assetsInlineLimit: 0,
    cssCodeSplit: false,
    sourcemap: false,
    rollupOptions: {
      input: { 'embed-admin': resolve(__dirname, 'embed-admin.html') },
    },
  },
})
