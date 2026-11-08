// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134, design §4/§9). Builds the page with its own config and checks
// the output against the page CSP: no inline script or style in the HTML, no eval or new Function in the
// bundle, no data: URI, every reference under /embed-admin/assets/, and only files the server will serve.
import assert from 'node:assert/strict'
import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { build } from 'vite'

const FRONTEND = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
// The extensions internal/router/agent24_embed_admin.go serves, and its file-name rule.
const SERVED = new Set(['.js', '.css', '.woff2', '.woff', '.ttf', '.png'])
const NAME = /^[A-Za-z0-9._-]{1,128}$/

let out = ''
let html = ''
let assets: string[] = []

before(async () => {
  out = mkdtempSync(join(tmpdir(), 'embed-admin-build-'))
  await build({
    configFile: join(FRONTEND, 'vite.embed-admin.config.ts'),
    root: FRONTEND,
    logLevel: 'silent',
    build: { outDir: out, emptyOutDir: true },
  })
  html = readFileSync(join(out, 'embed-admin.html'), 'utf8')
  assets = readdirSync(join(out, 'assets'))
})

after(() => {
  if (out) rmSync(out, { recursive: true, force: true })
})

test('output is embed-admin.html plus plain files directly under assets/', () => {
  assert.deepEqual(readdirSync(out).sort(), ['assets', 'embed-admin.html'])
  assert.ok(assets.length >= 2)
  for (const name of assets) {
    assert.ok(statSync(join(out, 'assets', name)).isFile(), name)
    assert.match(name, NAME)
    assert.notEqual(name[0], '.')
    assert.ok(SERVED.has(name.slice(name.lastIndexOf('.'))), name)
  }
})

test('the HTML has no inline script, style, handler or data: URI, and refers only to /embed-admin/assets/', () => {
  const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)]
  assert.ok(scripts.length >= 1)
  for (const [, attrs, body] of scripts) {
    assert.match(attrs, /\bsrc="\/embed-admin\/assets\/[^"]+\.js"/)
    assert.equal(body.trim(), '')
  }
  assert.equal(/<style\b/i.test(html), false)
  assert.equal(/\son[a-z]+\s*=/i.test(html), false)
  assert.equal(/\sstyle\s*=/i.test(html), false)
  assert.equal(html.includes('data:'), false)
  assert.equal(/<base\b|<iframe\b|<object\b|<embed\b|<form\b/i.test(html), false)
  for (const m of html.matchAll(/\s(?:src|href)="([^"]*)"/g)) {
    assert.ok(m[1].startsWith('/embed-admin/assets/'), m[1])
  }
})

test('the bundle has no eval, new Function, worker or data: URL in CSS', () => {
  for (const name of assets) {
    const text = readFileSync(join(out, 'assets', name), 'utf8')
    if (name.endsWith('.js')) {
      assert.equal(/\beval\s*\(|\bnew\s+Function\b|(?<![\w$.])Function\s*\(/.test(text), false, name)
      assert.equal(/\bimportScripts\b|\bnew\s+(?:Shared)?Worker\b|serviceWorker/.test(text), false, name)
    }
    if (name.endsWith('.css')) assert.equal(/url\(\s*['"]?data:/i.test(text), false, name)
  }
})
