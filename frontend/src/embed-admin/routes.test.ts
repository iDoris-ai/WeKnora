// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134, design §9). The host injects the session only for the exact
// list in docs/agent24/ui-routes.json, so every API path the page code names must be one of those GET paths.
// document_id belongs to the bridge only.
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { UI_ROUTES } from './routes.ts'

const HERE = dirname(fileURLToPath(import.meta.url))
const UI_ROUTES_JSON = join(HERE, '..', '..', '..', 'docs', 'agent24', 'ui-routes.json')

function sources(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...sources(p))
    else if (/\.(ts|vue)$/.test(name) && !name.endsWith('.test.ts')) out.push(p)
  }
  return out
}

function allowed(): Set<string> {
  const doc = JSON.parse(readFileSync(UI_ROUTES_JSON, 'utf8')) as { routes: Array<{ method: string; path: string }> }
  return new Set(doc.routes.filter((r) => r.method === 'GET').map((r) => r.path))
}

test('every route template is a GET path in ui-routes.json', () => {
  const ok = allowed()
  for (const p of Object.values(UI_ROUTES)) assert.ok(ok.has(p), p)
})

test('page code names no API path outside ui-routes.json, and no ticket, exchange, probe or revocation call', () => {
  const ok = allowed()
  const files = sources(HERE)
  assert.ok(files.length >= 5)
  for (const file of files) {
    const text = readFileSync(file, 'utf8')
    for (const m of text.matchAll(/['"`](\/api\/[^'"`]*)['"`]/g)) {
      assert.ok(ok.has(m[1]), `${relative(HERE, file)}: ${m[1]}`)
    }
    for (const word of ['/ui/tickets', '/ui/exchange', '/ui/session', '/ui/revocations', '/files', 'resource://']) {
      assert.equal(text.includes(word), false, `${relative(HERE, file)}: ${word}`)
    }
  }
})

test('document_id appears only in the bridge module, and the bridge fetches only the reserved path', () => {
  for (const file of sources(HERE)) {
    const name = relative(HERE, file)
    const text = readFileSync(file, 'utf8')
    if (name === 'bridge.ts') {
      const fetches = [...text.matchAll(/fetch\(([^,)]+)/g)].map((m) => m[1].trim())
      assert.deepEqual(fetches, ['BRIDGE_PATH'])
      continue
    }
    assert.equal(/document_?id/i.test(text), false, name)
  }
})

test('page code uses no storage, worker, keepalive or beacon', () => {
  for (const file of sources(HERE)) {
    const text = readFileSync(file, 'utf8')
    for (const word of ['localStorage', 'sessionStorage', 'indexedDB', 'document.cookie', 'serviceWorker',
      'new Worker', 'SharedWorker', 'keepalive', 'sendBeacon', 'pagehide', 'unload', 'innerHTML', 'outerHTML',
      'insertAdjacentHTML', 'document.write']) {
      assert.equal(text.includes(word), false, `${relative(HERE, file)}: ${word}`)
    }
  }
})

test('v-html appears only in MarkdownBlock.vue, on the sanitized render of renderMarkdown', () => {
  for (const file of sources(HERE)) {
    const name = relative(HERE, file)
    const text = readFileSync(file, 'utf8')
    if (name !== join('views', 'MarkdownBlock.vue')) {
      assert.equal(text.includes('v-html='), false, name)
      continue
    }
    assert.equal(text.match(/v-html=/g)?.length, 1)
    assert.match(text, /v-html="html"/)
    assert.match(text, /purify\.sanitize\(renderMarkdown\(/)
    assert.match(text, /ALLOWED_TAGS: \[\.\.\.SAFE_TAGS\]/)
    assert.match(text, /ALLOWED_ATTR: \[\.\.\.SAFE_ATTRS\]/)
    // KB-C2-02c: data-slug survives only on span.ea-md-wiki, and a click moves only to a slug in this page's set.
    assert.match(text, /data\.attrName === 'data-slug' && \(node\.nodeName !== 'SPAN' \|\| node\.getAttribute\('class'\) !== 'ea-md-wiki'\)/)
    assert.match(text, /if \(slug && props\.wikiLinks\?\.has\(slug\)\) emit\('wiki', slug\)/)
  }
})
