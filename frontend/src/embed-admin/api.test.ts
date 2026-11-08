// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). The page's HTTP client, design §4 and §9.
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { ApiError, getKnowledgeBase, listKnowledgeBases, setFetch } from './api.ts'
import { UI_ROUTES, uiPath } from './routes.ts'

interface Seen {
  url: string
  init: RequestInit
}

function fake(status: number, body: unknown, seen: Seen[]): void {
  setFetch(async (url, init) => {
    seen.push({ url, init })
    return new Response(body === undefined ? 'not json' : JSON.stringify(body), { status })
  })
}

test('requests are plain same-origin GETs: no credentials, no auth or tenant header, no bridge data', async () => {
  const seen: Seen[] = []
  fake(200, { knowledge_bases: [{ id: 'kb-1', name: 'A' }] }, seen)
  assert.equal((await listKnowledgeBases()).length, 1)
  fake(200, { knowledge_base: { id: 'kb/1', name: 'B' } }, seen)
  assert.equal((await getKnowledgeBase('kb/1')).name, 'B')

  assert.deepEqual(seen.map((s) => s.url), [
    '/api/v1/agent24/knowledge/ui/knowledge-bases',
    '/api/v1/agent24/knowledge/ui/knowledge-bases/kb%2F1',
  ])
  for (const { url, init } of seen) {
    assert.equal(init.method, 'GET')
    assert.equal(init.credentials, 'omit')
    assert.equal(init.cache, 'no-store')
    assert.equal(init.redirect, 'error')
    assert.equal(init.body, undefined)
    assert.deepEqual(init.headers, { Accept: 'application/json' })
    const all = url + JSON.stringify(init)
    for (const word of ['document_id', 'documentId', 'view_instance', 'Authorization', 'X-Tenant', 'X-API-Key']) {
      assert.equal(all.includes(word), false, word)
    }
  }
})

test('the error envelope becomes an ApiError with the bridge status code', async () => {
  const seen: Seen[] = []
  const cases: Array<[number, unknown, string, string | undefined, string]> = [
    [401, { error: { code: 'unauthorized', details: { reason: 'credential_expired' } } }, 'unauthorized', 'credential_expired', 'unauthorized'],
    [403, { error: { code: 'forbidden', details: { reason: 'empty_scope' } } }, 'forbidden', 'empty_scope', 'forbidden'],
    [404, { error: { code: 'not_found', details: {} } }, 'not_found', undefined, 'not_found'],
    [503, { error: { code: 'unavailable', details: { reason: 'not_ready' } } }, 'unavailable', 'not_ready', 'unavailable'],
    [504, { error: { code: 'timeout', details: {} } }, 'timeout', undefined, 'unavailable'],
    [400, { error: { code: 'invalid_request', details: {} } }, 'invalid_request', undefined, 'internal'],
    [401, undefined, 'unauthorized', undefined, 'unauthorized'],
    [502, undefined, 'internal', undefined, 'internal'],
  ]
  for (const [status, body, code, reason, statusCode] of cases) {
    fake(status, body, seen)
    await assert.rejects(listKnowledgeBases(), (e: unknown) => {
      assert.ok(e instanceof ApiError)
      assert.equal(e.code, code)
      assert.equal(e.reason, reason)
      assert.equal(e.statusCode, statusCode)
      return true
    })
  }
  // One request per call: nothing is retried.
  assert.equal(seen.length, cases.length)
})

test('a network failure is unavailable', async () => {
  setFetch(async () => {
    throw new TypeError('failed')
  })
  await assert.rejects(listKnowledgeBases(), (e: unknown) => e instanceof ApiError && e.statusCode === 'unavailable')
})

test('uiPath encodes each parameter as one segment and refuses an empty one', () => {
  assert.equal(uiPath(UI_ROUTES.kbGet, { kb_id: 'a b/c?d#e' }), '/api/v1/agent24/knowledge/ui/knowledge-bases/a%20b%2Fc%3Fd%23e')
  assert.throws(() => uiPath(UI_ROUTES.kbGet, {}))
  assert.throws(() => uiPath(UI_ROUTES.kbGet, { kb_id: '' }))
})
