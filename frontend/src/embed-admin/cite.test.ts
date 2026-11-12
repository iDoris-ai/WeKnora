// Agent24 fork A24-KB-39 (KB-C2-02d, WeKnora#134). Citation references and the document view's actions.
import assert from 'node:assert/strict'
import { test } from 'node:test'

import type { Chunk, DocumentDetail } from './api.ts'
import {
  CiteActions,
  citationRefOf,
  isCitationRef,
  refIdOk,
  toggleRef,
  type ActionReply,
  type CitationRef,
  type CiteBridge,
  type Trigger,
} from './cite.ts'

const DOC: DocumentDetail = {
  knowledge_id: '50f957f8-fa43-4816-a7c6-8c3c93a44764',
  knowledge_base_id: '439127c1-8157-4095-997d-120a87ed83b7',
  title: 't',
  file_name: 'f.md',
  parse_status: 'completed',
  revision: { source_revision: 'a'.repeat(64), index_build_id: 'b'.repeat(64) },
  chunk_count: 2,
}
const CHUNK: Chunk = {
  chunk_id: '0b0b0b0b-0000-4000-8000-000000000001',
  position: 1,
  chunk_revision: 3,
  content_tag: 'c'.repeat(64),
  text: 'hello',
}
const REF: CitationRef = {
  knowledge_base_id: DOC.knowledge_base_id,
  knowledge_id: DOC.knowledge_id,
  chunk_id: CHUNK.chunk_id,
  source_revision: 'a'.repeat(64),
  index_build_id: 'b'.repeat(64),
  chunk_revision: 3,
  content_tag: 'c'.repeat(64),
}
const ref = (n: number): CitationRef => ({ ...REF, chunk_id: `c-${n}` })
const CLICK: Trigger = { isTrusted: true, type: 'click' }

test('refIdOk: 1–128 UTF-8 bytes, no control character, no / \\ : .. or whitespace', () => {
  for (const ok of ['a', 'x'.repeat(128), 'a.b', 'a-b_c', 'A1', '中'.repeat(42), 'é']) assert.equal(refIdOk(ok), true, ok)
  for (const bad of ['', 'x'.repeat(129), '中'.repeat(43), 'a/b', 'a\\b', 'a:b', '..', 'a..b', 'a b', 'a\tb', 'a\nb',
    'a b', 'a b', 'a\u0000b', 'a\u001fb', 'a\u007fb', 'a\u0085b', 'a\u009fb', 1, null, undefined, {}]) {
    assert.equal(refIdOk(bad), false, JSON.stringify(bad))
  }
})

test('isCitationRef: exactly the seven keys, valid ids, chunk_revision an integer in 0..2^31−1', () => {
  assert.equal(isCitationRef(REF), true)
  assert.equal(isCitationRef({ ...REF, chunk_revision: 0 }), true)
  assert.equal(isCitationRef({ ...REF, chunk_revision: 2 ** 31 - 1 }), true)
  for (const bad of [
    { ...REF, chunk_revision: -1 }, { ...REF, chunk_revision: 2 ** 31 }, { ...REF, chunk_revision: 1.5 },
    { ...REF, chunk_revision: '3' }, { ...REF, chunk_revision: Number.NaN }, { ...REF, extra: 1 },
    { ...REF, chunk_id: 'a:b' }, { ...REF, content_tag: '' }, (({ chunk_id: _c, ...rest }) => rest)(REF),
    null, [], 'x', [REF],
  ]) assert.equal(isCitationRef(bad), false, JSON.stringify(bad))
  const proto = Object.create({ chunk_revision: 3 })
  Object.assign(proto, (({ chunk_revision: _r, ...rest }) => rest)(REF))
  assert.equal(isCitationRef(proto), false)
  // All seven own keys, but not a plain object.
  const odd = Object.assign(Object.create({ extra: 1 }), REF)
  assert.deepEqual(Object.keys(odd).sort(), Object.keys(REF).sort())
  assert.equal(isCitationRef(odd), false)
  class Ref { constructor() { Object.assign(this, REF) } }
  assert.equal(isCitationRef(new Ref()), false)
})

test('citationRefOf takes the ids from doc.get and chunk.list, and copies only the seven fields', () => {
  const r = citationRefOf(DOC, CHUNK)
  assert.deepEqual(r, REF)
  assert.notEqual(r, CHUNK as unknown)
  assert.deepEqual(Object.keys(r!).sort(), Object.keys(REF).sort())
})

test('citationRefOf is null without an index build or with any id the host would refuse', () => {
  assert.equal(citationRefOf({ ...DOC, revision: { ...DOC.revision, index_build_id: null } }, CHUNK), null)
  assert.equal(citationRefOf({ ...DOC, knowledge_id: 'a/b' }, CHUNK), null)
  assert.equal(citationRefOf(DOC, { ...CHUNK, chunk_id: 'x y' }), null)
  assert.equal(citationRefOf(DOC, { ...CHUNK, content_tag: '' }), null)
  assert.equal(citationRefOf(DOC, { ...CHUNK, chunk_revision: -2 }), null)
  assert.equal(citationRefOf({ ...DOC, revision: { ...DOC.revision, source_revision: 's:1' } }, CHUNK), null)
})

test('toggleRef adds in ticking order, removes by chunk_id, stops at max, never mutates', () => {
  const empty: CitationRef[] = []
  const one = toggleRef(empty, ref(1), 2)
  assert.deepEqual(empty, [])
  const two = toggleRef(one, ref(2), 2)
  assert.deepEqual(two.map((r) => r.chunk_id), ['c-1', 'c-2'])
  assert.deepEqual(toggleRef(two, ref(3), 2).map((r) => r.chunk_id), ['c-1', 'c-2'])
  assert.deepEqual(toggleRef(two, { ...ref(1), chunk_revision: 9 }, 2).map((r) => r.chunk_id), ['c-2'])
  assert.deepEqual(one.map((r) => r.chunk_id), ['c-1'])
})

// ── CiteActions ───────────────────────────────────────────────────────────────

class FakeBridge implements CiteBridge {
  calls: string[] = []
  refuse = false
  private n = 0
  private resolvers = new Map<string, (r: ActionReply | null) => void>()
  private send(kind: string): { id: string; reply: Promise<ActionReply | null> } | null {
    if (this.refuse) return null
    const id = `ID${++this.n}`
    this.calls.push(`${kind}:${id}`)
    return { id, reply: new Promise((res) => this.resolvers.set(id, res)) }
  }
  cite(_ref: CitationRef, _t: Trigger) {
    return this.send('cite')
  }
  returnToChat(_refs: readonly CitationRef[], _t: Trigger) {
    return this.send('intent')
  }
  cancel(targetId: string): Promise<ActionReply | null> {
    this.calls.push(`cancel:${targetId}`)
    return Promise.resolve({ result: 'accepted' })
  }
  answer(id: string, r: ActionReply | null): void {
    this.resolvers.get(id)?.(r)
  }
}
const tick = () => new Promise((r) => setTimeout(r, 0))

test('a citation that ends in done without a code is sent; with a code or rejected it failed', async () => {
  const b = new FakeBridge()
  let changes = 0
  const a = new CiteActions(b, () => changes++)
  a.cite(REF, CLICK)
  b.answer('ID1', { result: 'done' })
  await tick()
  assert.deepEqual(a.outcome, { kind: 'sent' })
  a.cite(REF, CLICK)
  b.answer('ID2', { result: 'done', code: 'stale' })
  await tick()
  assert.deepEqual(a.outcome, { kind: 'failed', code: 'stale' })
  a.cite(REF, CLICK)
  b.answer('ID3', { result: 'rejected', code: 'forbidden', reason: 'cooldown' })
  await tick()
  assert.deepEqual(a.outcome, { kind: 'failed', code: 'cooldown' })
  assert.ok(changes >= 3)
})

test('a refused send is not_sent; a null reply clears the outcome', async () => {
  const b = new FakeBridge()
  const a = new CiteActions(b, () => {})
  b.refuse = true
  a.cite(REF, CLICK)
  assert.deepEqual(a.outcome, { kind: 'not_sent' })
  a.returnToChat([REF], CLICK)
  assert.deepEqual(a.outcome, { kind: 'not_sent' })
  assert.equal(a.intentPending, false)
  b.refuse = false
  a.cite(REF, CLICK)
  b.answer('ID1', null)
  await tick()
  assert.equal(a.outcome, null)
})

test('an intent waits, can be cancelled, and is no longer pending after its terminal reply', async () => {
  const b = new FakeBridge()
  const a = new CiteActions(b, () => {})
  a.returnToChat([REF, ref(2)], CLICK)
  assert.equal(a.intentPending, true)
  assert.deepEqual(a.outcome, { kind: 'waiting' })
  a.cancelIntent()
  assert.deepEqual(b.calls, ['intent:ID1', 'cancel:ID1'])
  b.answer('ID1', { result: 'rejected', code: 'cancelled' })
  await tick()
  assert.equal(a.intentPending, false)
  assert.deepEqual(a.outcome, { kind: 'failed', code: 'cancelled' })
  a.cancelIntent()
  assert.deepEqual(b.calls, ['intent:ID1', 'cancel:ID1'])
})

test('dispose cancels every citation and intent still waiting, once, and ignores later replies', async () => {
  const b = new FakeBridge()
  let changes = 0
  const a = new CiteActions(b, () => changes++)
  a.cite(REF, CLICK)
  a.cite(ref(2), CLICK)
  a.returnToChat([REF], CLICK)
  b.answer('ID1', { result: 'done' })
  await tick()
  a.dispose()
  a.dispose()
  assert.deepEqual(b.calls.filter((c) => c.startsWith('cancel:')).sort(), ['cancel:ID2', 'cancel:ID3'])
  const before = changes
  b.answer('ID2', { result: 'done' })
  b.answer('ID3', { result: 'done' })
  await tick()
  assert.equal(changes, before)
})
