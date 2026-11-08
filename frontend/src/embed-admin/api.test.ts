// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). The page's HTTP client, design §4 and §9.
import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  ApiError, getDocument, getDocumentIn, getKnowledgeBase, getWikiPage, listChunks, listDocuments, listKnowledgeBases,
  listTags, listWikiPages, setFetch,
} from './api.ts'
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

test('documents, chunks and tags: paths, page query, and a page shape that tolerates missing fields', async () => {
  const seen: Seen[] = []
  fake(200, { documents: [{ knowledge_id: 'k1' }], total: 41, page: 3, page_size: 20 }, seen)
  const docs = await listDocuments('kb 1', 3)
  assert.deepEqual([docs.items.length, docs.total, docs.page, docs.pageSize], [1, 41, 3, 20])
  fake(200, { document: { knowledge_id: 'k/1', revision: { source_revision: 'r', index_build_id: null } } }, seen)
  assert.equal((await getDocument('k/1')).knowledge_id, 'k/1')
  fake(200, { chunks: [{ chunk_id: 'c' }], total: 1, page: 1, page_size: 25 }, seen)
  assert.equal((await listChunks('k/1', 2)).items.length, 1)
  fake(200, { tags: 'not a list', total: -5, page: 'x' }, seen)
  const tags = await listTags('kb 1', 1)
  assert.deepEqual([tags.items, tags.total, tags.page, tags.pageSize], [[], 0, 1, 1])
  fake(200, {}, seen)
  await assert.rejects(getDocument('k1'), (e: unknown) => e instanceof ApiError && e.code === 'internal')

  assert.deepEqual(seen.map((s) => s.url), [
    '/api/v1/agent24/knowledge/ui/knowledge-bases/kb%201/documents?page=3',
    '/api/v1/agent24/knowledge/ui/documents/k%2F1',
    '/api/v1/agent24/knowledge/ui/documents/k%2F1/chunks?page=2',
    '/api/v1/agent24/knowledge/ui/knowledge-bases/kb%201/tags?page=1',
    '/api/v1/agent24/knowledge/ui/documents/k1',
  ])
  for (const { init } of seen) assert.equal(init.credentials, 'omit')
})

test('a document of another knowledge base is not_found under this one (Codex b-r1 C2)', async () => {
  const seen: Seen[] = []
  fake(200, { document: { knowledge_id: 'k1', knowledge_base_id: 'kbB' } }, seen)
  await assert.rejects(getDocumentIn('kbA', 'k1'), (e: unknown) => e instanceof ApiError && e.code === 'not_found')
  fake(200, { document: { knowledge_id: 'k1', knowledge_base_id: 'kbA' } }, seen)
  assert.equal((await getDocumentIn('kbA', 'k1')).knowledge_id, 'k1')
})

// KB-C2-02c: wiki.
test('wiki.get keeps the slashes of a slug and encodes each part (no %2F); dot and empty parts request nothing', async () => {
  const seen: Seen[] = []
  fake(200, { page: { slug: 'entity/acme-corp', out_links: ['a', 3], in_links: null, aliases: 'x' } }, seen)
  const p = await getWikiPage('kb1', 'entity/acme corp?')
  assert.deepEqual([p.out_links, p.in_links, p.aliases, p.source_documents], [['a'], [], [], []])
  fake(200, { pages: [{ slug: 's' }], total: 1, page: 1, page_size: 20 }, seen)
  assert.equal((await listWikiPages('kb1', 1)).items.length, 1)
  assert.deepEqual(seen.map((s) => s.url), [
    '/api/v1/agent24/knowledge/ui/knowledge-bases/kb1/wiki/pages/entity/acme%20corp%3F',
    '/api/v1/agent24/knowledge/ui/knowledge-bases/kb1/wiki/pages?page=1',
  ])
  assert.equal(seen[0].url.includes('%2F'), false)

  for (const bad of ['', '/a', 'a/', 'a//b', '.', '..', 'a/../b', 'a/./b', '../../tags']) {
    const before = seen.length
    await assert.rejects(getWikiPage('kb1', bad), (e: unknown) => e instanceof ApiError && e.code === 'not_found', bad)
    assert.equal(seen.length, before, bad)
  }
  for (const bad of ['.', '..']) {
    const before = seen.length
    await assert.rejects(getKnowledgeBase(bad), (e: unknown) => e instanceof ApiError && e.code === 'not_found', bad)
    assert.equal(seen.length, before, bad)
  }
})

test('uiPath: one segment per :param, slash-kept parts for *slug', () => {
  assert.equal(uiPath(UI_ROUTES.wikiGet, { kb_id: 'k', slug: 'a/b c/%2F' }),
    '/api/v1/agent24/knowledge/ui/knowledge-bases/k/wiki/pages/a/b%20c/%252F')
  assert.equal(uiPath(UI_ROUTES.wikiGet, { kb_id: 'k/x', slug: 'a' }), '/api/v1/agent24/knowledge/ui/knowledge-bases/k%2Fx/wiki/pages/a')
})
