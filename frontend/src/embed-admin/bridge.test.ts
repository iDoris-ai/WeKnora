// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). The page side of the bridge, design §9.
import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  BRIDGE_VERSION,
  CITATION_TIMEOUT_MS,
  HELLO_REQUESTS_MAX,
  HELLO_RETRY_MS,
  IN_FLIGHT_MAX,
  INTENT_CITATIONS_MAX,
  INTENT_TIMEOUT_MS,
  BRIDGE_MAX_MESSAGE_BYTES,
  OVERLOAD_BACKOFF_MAX_MS,
  OVERLOAD_BACKOFF_MS,
  PageBridge,
  REPLY_TIMEOUT_MS,
  ulid,
  type BridgeHost,
  type CitationRef,
} from './bridge.ts'

const DOC_A = 'AAAAAAAAAAAAAAAAAAAAAA'
const DOC_B = 'BBBBBBBBBBBBBBBBBBBBBB'
const ULID = /^[0-9A-HJKMNP-TV-Z]{26}$/

class FakeHost implements BridgeHost {
  sent: Record<string, unknown>[] = []
  private listener: ((d: unknown) => void) | null = null
  private timers = new Map<number, { at: number; fn: () => void }>()
  private next = 1
  clock = 1_700_000_000_000
  private seq = 0
  /** navigator.userActivation.isActive, as the fake page has it. */
  active = true

  send(body: string): void {
    this.sent.push(JSON.parse(body))
  }
  listen(onData: (d: unknown) => void): () => void {
    this.listener = onData
    return () => {
      this.listener = null
    }
  }
  setTimer(fn: () => void, ms: number): unknown {
    const h = this.next++
    this.timers.set(h, { at: this.clock + ms, fn })
    return h
  }
  clearTimer(handle: unknown): void {
    this.timers.delete(handle as number)
  }
  now(): number {
    return this.clock
  }
  random(bytes: Uint8Array<ArrayBuffer>): void {
    this.seq++
    bytes.fill(this.seq % 256)
  }
  userActive(): boolean {
    return this.active
  }
  advance(ms: number): void {
    const until = this.clock + ms
    for (;;) {
      const due = [...this.timers.entries()].filter(([, t]) => t.at <= until).sort((a, b) => a[1].at - b[1].at)[0]
      if (!due) break
      this.timers.delete(due[0])
      this.clock = due[1].at
      due[1].fn()
    }
    this.clock = until
  }
  deliver(data: unknown): void {
    this.listener?.(data)
  }
  hello(doc: string, generation = 1, supported: number[] = [3]): void {
    this.deliver({
      v: 3, direction: 'host_to_page', type: 'hello', id: ulid(this.clock, (b) => b.fill(1)), generation,
      document_id: doc, payload: { supported, view_instance_id: 'VVVVVVVVVVVVVVVVVVVVVV' },
    })
  }
  reply(doc: string, inReplyTo: string, result = 'done', extra: Record<string, unknown> = {}): void {
    this.deliver({
      v: 3, direction: 'host_to_page', type: 'reply', id: ulid(this.clock, (b) => b.fill(2)), generation: 1,
      document_id: doc, in_reply_to: inReplyTo, payload: { result, ...extra },
    })
  }
}

function started(): { host: FakeHost; bridge: PageBridge } {
  const host = new FakeHost()
  const bridge = new PageBridge(host)
  bridge.start()
  return { host, bridge }
}

test('ulid has the contract shape', () => {
  assert.match(ulid(Date.now(), (b) => crypto.getRandomValues(b)), ULID)
  assert.match(ulid(0, (b) => b.fill(255)), ULID)
})

test('before any hello only the hello request goes out: generation 0 status, v 3, no document_id, no supported', () => {
  const { host, bridge } = started()
  bridge.setStatus({ state: 'ready' })
  assert.equal(host.sent.length, 1)
  const hello = host.sent[0]
  assert.equal(hello.v, BRIDGE_VERSION)
  assert.equal(hello.direction, 'page_to_host')
  assert.equal(hello.type, 'status')
  assert.equal(hello.generation, 0)
  assert.match(hello.id as string, ULID)
  assert.equal('document_id' in hello, false)
  assert.equal('in_reply_to' in hello, false)
  assert.deepEqual(hello.payload, { state: 'loading' })
  assert.equal(JSON.stringify(host.sent).includes('supported'), false)
})

test('the hello request is repeated at most three times without a hello, then stops', () => {
  const { host } = started()
  host.advance(HELLO_RETRY_MS * 10)
  assert.equal(host.sent.length, HELLO_REQUESTS_MAX)
  for (const m of host.sent) assert.equal(m.generation, 0)
})

test('a hello stops the retries and sends the current status with that document_id and generation', () => {
  const { host, bridge } = started()
  bridge.setStatus({ state: 'ready' })
  host.hello(DOC_A, 7)
  host.advance(HELLO_RETRY_MS * 10)
  assert.equal(host.sent.length, 2)
  const status = host.sent[1]
  assert.equal(status.type, 'status')
  assert.equal(status.document_id, DOC_A)
  assert.equal(status.generation, 7)
  assert.deepEqual(status.payload, { state: 'ready' })
})

test('two hellos with different ids: everything after carries the second id (last hello wins)', () => {
  const { host, bridge } = started()
  host.hello(DOC_A)
  host.hello(DOC_B)
  bridge.setStatus({ state: 'error', error_code: 'unauthorized' })
  const after = host.sent.slice(1)
  assert.ok(after.length >= 2)
  assert.equal(after.at(-1)!.document_id, DOC_B)
  assert.deepEqual(after.at(-1)!.payload, { state: 'error', error_code: 'unauthorized' })
  // Every message except the generation-0 hello request carries a document_id.
  for (const m of host.sent) {
    if (m.generation === 0) continue
    assert.equal(typeof m.document_id, 'string')
  }
})

test('a repeated hello of the same document does not resend the status', () => {
  const { host } = started()
  host.hello(DOC_A)
  host.hello(DOC_A)
  assert.equal(host.sent.length, 2)
})

test('a request that gets no reply is given up after the timeout, silently', async () => {
  const { host, bridge } = started()
  host.hello(DOC_A)
  const before = bridge.pendingCount
  const p = bridge.request('status', { state: 'ready' })
  assert.equal(bridge.pendingCount, before + 1)
  host.advance(REPLY_TIMEOUT_MS - 1)
  assert.equal(bridge.pendingCount, before + 1)
  host.advance(1)
  assert.equal(await p, null)
  assert.equal(bridge.pendingCount, 0)
})

test('a reply resolves its request; one for another document or an unknown id does not', async () => {
  const { host, bridge } = started()
  host.hello(DOC_A)
  const p = bridge.request('status', { state: 'ready' })
  const id = host.sent.at(-1)!.id as string
  host.reply(DOC_B, id)
  host.reply(DOC_A, 'NOTAPENDINGID000000000000A')
  host.reply(DOC_A, id, 'accepted')
  assert.ok(bridge.pendingCount >= 1)
  host.reply(DOC_A, id, 'done')
  assert.deepEqual(await p, { result: 'done' })
})

test('a new hello clears every pending request', async () => {
  const { host, bridge } = started()
  host.hello(DOC_A)
  const p1 = bridge.request('status', { state: 'loading' })
  const p2 = bridge.request('status', { state: 'ready' })
  assert.ok(bridge.pendingCount >= 2)
  host.hello(DOC_B)
  assert.equal(await p1, null)
  assert.equal(await p2, null)
  // Only the status sent for the new document is pending now.
  assert.equal(bridge.pendingCount, 1)
  assert.equal(host.sent.at(-1)!.document_id, DOC_B)
})

test('a hello without version 3 leaves the bridge closed: nothing more is sent, not even hello requests', () => {
  const { host, bridge } = started()
  host.hello(DOC_A, 1, [2])
  bridge.setStatus({ state: 'ready' })
  host.advance(HELLO_RETRY_MS * 10)
  assert.equal(bridge.ready, false)
  assert.equal(host.sent.length, 1)
})

test('a request before any hello sends nothing and resolves null', async () => {
  const { host, bridge } = started()
  assert.equal(await bridge.request('status', { state: 'ready' }), null)
  assert.equal(host.sent.length, 1)
  assert.equal(bridge.pendingCount, 0)
})

test('malformed or page-to-host data is ignored', () => {
  const { host, bridge } = started()
  for (const d of [null, 'x', 3, [], {}, { v: 2, direction: 'host_to_page', type: 'hello' },
    { v: 3, direction: 'page_to_host', type: 'hello', id: 'x', generation: 1, document_id: DOC_A, payload: { supported: [3] } },
    { v: 3, direction: 'host_to_page', type: 'hello', id: 'x', generation: 1, payload: { supported: [3] } },
    { v: 3, direction: 'host_to_page', type: 'hello', id: 'x', generation: 0, document_id: DOC_A, payload: { supported: [3] } },
  ]) host.deliver(d)
  assert.equal(bridge.ready, false)
  assert.equal(host.sent.length, 1)
})

test('state of the current document reaches listeners; state of another does not', () => {
  const { host, bridge } = started()
  const seen: string[] = []
  bridge.onState((s) => seen.push(`${s.theme}/${s.locale}`))
  host.hello(DOC_A)
  const state = (doc: string, theme: string) => host.deliver({
    v: 3, direction: 'host_to_page', type: 'state', id: 'S', generation: 1, document_id: doc,
    payload: { profile_state: 'ready', theme, locale: 'en-US' },
  })
  state(DOC_A, 'dark')
  state(DOC_B, 'light')
  state(DOC_A, 'blue')
  assert.deepEqual(seen, ['dark/en-US'])
})

// ── KB-C2-02d: citation, return_to_chat_intent, cancel ────────────────────────

const CLICK = { isTrusted: true, type: 'click' }
const REF: CitationRef = {
  knowledge_base_id: '439127c1-8157-4095-997d-120a87ed83b7',
  knowledge_id: '50f957f8-fa43-4816-a7c6-8c3c93a44764',
  chunk_id: '0b0b0b0b-0000-4000-8000-000000000001',
  source_revision: 'a'.repeat(64),
  index_build_id: 'b'.repeat(64),
  chunk_revision: 3,
  content_tag: 'c'.repeat(64),
}
const ref = (n: number): CitationRef => ({ ...REF, chunk_id: `0b0b0b0b-0000-4000-8000-${String(n).padStart(12, '0')}` })

/** Open on DOC_A, with the status sent on hello answered so only what a test sends is pending. */
function opened(): { host: FakeHost; bridge: PageBridge } {
  const s = started()
  s.host.hello(DOC_A)
  s.host.reply(DOC_A, s.host.sent.at(-1)!.id as string)
  return s
}
const sentOf = (host: FakeHost, type: string) => host.sent.filter((m) => m.type === type)

/** UIB U7.4, compared the way the host compares (lower-case, no `_` or `-`, whole key). */
const FORBIDDEN = new Set(['url', 'uri', 'href', 'path', 'file', 'dir', 'cwd', 'command', 'cmd', 'exec', 'executable',
  'argv', 'args', 'shell', 'script', 'approval', 'approve', 'decision', 'token', 'jwt', 'ticket', 'secret', 'password',
  'authorization', 'cookie', 'origin', 'sender', 'frame', 'webcontents', 'port', '__proto__', 'constructor', 'prototype',
].map((k) => k.replace(/[_-]/g, '')))
function forbiddenKeys(v: unknown, out: string[] = []): string[] {
  if (Array.isArray(v)) for (const x of v) forbiddenKeys(x, out)
  else if (typeof v === 'object' && v !== null) {
    for (const [k, x] of Object.entries(v)) {
      if (FORBIDDEN.has(k.toLowerCase().replace(/[_-]/g, ''))) out.push(k)
      forbiddenKeys(x, out)
    }
  }
  return out
}

test('citation: the payload is exactly the seven fields, with the current document_id and generation', () => {
  const { host, bridge } = opened()
  const sent = bridge.cite(REF, CLICK)
  assert.ok(sent)
  const m = sentOf(host, 'citation')
  assert.equal(m.length, 1)
  assert.equal(m[0].id, sent.id)
  assert.match(sent.id, ULID)
  assert.equal(m[0].v, 3)
  assert.equal(m[0].direction, 'page_to_host')
  assert.equal(m[0].document_id, DOC_A)
  assert.equal(m[0].generation, 1)
  assert.deepEqual(Object.keys(m[0]).sort(), ['direction', 'document_id', 'generation', 'id', 'payload', 'type', 'v'])
  assert.deepEqual(m[0].payload, REF)
  assert.deepEqual(Object.keys(m[0].payload as object).sort(),
    ['chunk_id', 'chunk_revision', 'content_tag', 'index_build_id', 'knowledge_base_id', 'knowledge_id', 'source_revision'])
})

test('citation and intent need a trusted click and user activation; otherwise nothing is sent', () => {
  const { host, bridge } = opened()
  const before = host.sent.length
  for (const t of [{ isTrusted: false, type: 'click' }, { isTrusted: true, type: 'keydown' },
    { isTrusted: true, type: 'pointerdown' }, { isTrusted: false, type: 'keydown' }]) {
    assert.equal(bridge.cite(REF, t), null)
    assert.equal(bridge.returnToChat([REF], t), null)
  }
  host.active = false
  assert.equal(bridge.cite(REF, CLICK), null)
  assert.equal(bridge.returnToChat([REF], CLICK), null)
  assert.equal(host.sent.length, before)
  assert.equal(bridge.pendingCount, 0)
  host.active = true
  assert.ok(bridge.cite(REF, CLICK))
  assert.ok(bridge.returnToChat([REF], CLICK))
})

test('the trigger is read when called: a fake event object built by page code is not trusted', () => {
  const { host, bridge } = opened()
  const before = host.sent.length
  // What a script would forge; the real Event.isTrusted is a read-only getter set by the browser.
  const forged = new Event('click')
  assert.equal(forged.isTrusted, false)
  assert.equal(bridge.cite(REF, forged), null)
  assert.equal(bridge.returnToChat([REF], forged), null)
  assert.equal(host.sent.length, before)
})

test('nothing is sent before the bridge is open', () => {
  const { host, bridge } = started()
  assert.equal(bridge.cite(REF, CLICK), null)
  assert.equal(bridge.returnToChat([REF], CLICK), null)
  assert.equal(host.sent.length, 1)
})

test('an invalid reference is never sent, alone or inside an intent', () => {
  const { host, bridge } = opened()
  const before = host.sent.length
  const bad: unknown[] = [
    { ...REF, chunk_id: 'a/b' }, { ...REF, chunk_id: 'a\\b' }, { ...REF, knowledge_id: 'k:1' },
    { ...REF, content_tag: 'x..y' }, { ...REF, source_revision: 'a b' }, { ...REF, index_build_id: 'a\tb' },
    { ...REF, knowledge_base_id: '' }, { ...REF, chunk_id: 'x'.repeat(129) }, { ...REF, chunk_id: 'a\u0001b' },
    { ...REF, chunk_id: 'a\u0085b' }, { ...REF, chunk_revision: -1 }, { ...REF, chunk_revision: 1.5 },
    { ...REF, chunk_revision: 2 ** 31 }, { ...REF, extra: 'x' }, { ...REF, url: 'x' },
    (({ content_tag: _c, ...rest }) => rest)(REF),
  ]
  for (const b of bad) {
    assert.equal(bridge.cite(b as CitationRef, CLICK), null)
    assert.equal(bridge.returnToChat([REF, b as CitationRef], CLICK), null)
  }
  assert.equal(host.sent.length, before)
})

test('intent: payload is { citations } with 0–8 references; more than 8 is not sent', () => {
  const { host, bridge } = opened()
  const refs = Array.from({ length: INTENT_CITATIONS_MAX }, (_, i) => ref(i + 1))
  assert.equal(bridge.returnToChat([...refs, ref(99)], CLICK), null)
  assert.equal(sentOf(host, 'return_to_chat_intent').length, 0)
  const sent = bridge.returnToChat(refs, CLICK)
  assert.ok(sent)
  const m = sentOf(host, 'return_to_chat_intent')
  assert.equal(m.length, 1)
  assert.deepEqual(m[0].payload, { citations: refs })
  assert.equal(m[0].document_id, DOC_A)
})

test('no forbidden key at any depth in anything the page sends', () => {
  const { host, bridge } = opened()
  bridge.setStatus({ state: 'error', error_code: 'unauthorized' })
  const c = bridge.cite(REF, CLICK)!
  const i = bridge.returnToChat([ref(1), ref(2)], CLICK)!
  void bridge.cancel(i.id)
  void bridge.cancel(c.id)
  assert.deepEqual(new Set(host.sent.map((m) => m.type)), new Set(['status', 'citation', 'return_to_chat_intent', 'cancel']))
  for (const m of host.sent) assert.deepEqual(forbiddenKeys(m), [], JSON.stringify(m))
})

test('accepted is not terminal; done resolves the citation with the reply', async () => {
  const { host, bridge } = opened()
  const sent = bridge.cite(REF, CLICK)!
  host.reply(DOC_A, sent.id, 'accepted')
  assert.equal(bridge.pendingCount, 1)
  host.reply(DOC_A, sent.id, 'done', { code: 'stale' })
  assert.deepEqual(await sent.reply, { result: 'done', code: 'stale' })
  assert.equal(bridge.pendingCount, 0)
})

test('citation gives up after CITATION_TIMEOUT_MS, intent after INTENT_TIMEOUT_MS, silently', async () => {
  const { host, bridge } = opened()
  const c = bridge.cite(REF, CLICK)!
  const i = bridge.returnToChat([REF], CLICK)!
  host.reply(DOC_A, c.id, 'accepted')
  host.reply(DOC_A, i.id, 'accepted')
  host.advance(CITATION_TIMEOUT_MS - 1)
  assert.equal(bridge.pendingCount, 2)
  host.advance(1)
  assert.equal(await c.reply, null)
  assert.equal(bridge.pendingCount, 1)
  host.advance(INTENT_TIMEOUT_MS - CITATION_TIMEOUT_MS - 1)
  assert.equal(bridge.pendingCount, 1)
  host.advance(1)
  assert.equal(await i.reply, null)
  assert.equal(bridge.pendingCount, 0)
  assert.ok(INTENT_TIMEOUT_MS > 60_000 && CITATION_TIMEOUT_MS > REPLY_TIMEOUT_MS)
})

test('at most one intent at a time', () => {
  const { host, bridge } = opened()
  const first = bridge.returnToChat([REF], CLICK)
  assert.ok(first)
  assert.equal(bridge.canReturnToChat, false)
  assert.equal(bridge.returnToChat([ref(2)], CLICK), null)
  assert.equal(sentOf(host, 'return_to_chat_intent').length, 1)
  assert.equal(bridge.canCite, true)
  host.reply(DOC_A, first.id, 'done')
  assert.equal(bridge.canReturnToChat, true)
})

test('at most IN_FLIGHT_MAX citations and intents pending; status and cancel do not count', () => {
  const { host, bridge } = opened()
  const ids: string[] = []
  for (let n = 0; n < IN_FLIGHT_MAX; n++) {
    bridge.setStatus({ state: 'ready' })
    ids.push(bridge.cite(ref(n + 1), CLICK)!.id)
  }
  assert.equal(bridge.canCite, false)
  assert.equal(bridge.canReturnToChat, false)
  assert.equal(bridge.cite(ref(50), CLICK), null)
  assert.equal(bridge.returnToChat([ref(51)], CLICK), null)
  assert.equal(sentOf(host, 'citation').length, IN_FLIGHT_MAX)
  void bridge.cancel(ids[0])
  assert.equal(bridge.canCite, false)
  host.reply(DOC_A, ids[0], 'rejected', { code: 'cancelled' })
  assert.equal(bridge.canCite, true)
})

test('after rejected/unavailable/overloaded nothing goes out until retry_after_ms has passed', () => {
  const { host, bridge } = opened()
  const statuses = sentOf(host, 'status').length
  const a = bridge.cite(REF, CLICK)!
  host.reply(DOC_A, a.id, 'rejected', { code: 'unavailable', reason: 'overloaded', retry_after_ms: 2500 })
  assert.equal(bridge.canCite, false)
  assert.equal(bridge.canReturnToChat, false)
  assert.equal(bridge.cite(ref(2), CLICK), null)
  assert.equal(bridge.returnToChat([ref(3)], CLICK), null)
  host.advance(2499)
  assert.equal(bridge.cite(ref(2), CLICK), null)
  host.advance(1)
  assert.equal(bridge.canCite, true)
  assert.ok(bridge.cite(ref(2), CLICK))
  assert.equal(sentOf(host, 'citation').length, 2)
  // A refused citation is not resent, and it does not make a status go out.
  assert.equal(sentOf(host, 'status').length, statuses)
})

test('an overloaded reply without retry_after_ms backs off OVERLOAD_BACKOFF_MS; other rejections do not back off', () => {
  const { host, bridge } = opened()
  const a = bridge.returnToChat([REF], CLICK)!
  host.reply(DOC_A, a.id, 'rejected', { code: 'forbidden', reason: 'cooldown' })
  assert.equal(bridge.canReturnToChat, true)
  const b = bridge.cite(REF, CLICK)!
  host.reply(DOC_A, b.id, 'rejected', { code: 'unavailable', reason: 'overloaded' })
  assert.equal(bridge.canCite, false)
  host.advance(OVERLOAD_BACKOFF_MS - 1)
  assert.equal(bridge.canCite, false)
  host.advance(1)
  assert.equal(bridge.canCite, true)
})

/** Settles to the value, or to 'unsettled' if the promise has not resolved after the microtask queue drains. */
async function settled<T>(p: Promise<T>): Promise<T | 'unsettled'> {
  return Promise.race([p, new Promise<'unsettled'>((r) => setTimeout(() => r('unsettled'), 0))])
}

test('PR-Daemon #170 L1: retry_after_ms is capped at OVERLOAD_BACKOFF_MAX_MS', () => {
  const { host, bridge } = opened()
  const a = bridge.cite(REF, CLICK)!
  host.reply(DOC_A, a.id, 'rejected', { code: 'unavailable', reason: 'overloaded', retry_after_ms: 1e12 })
  host.advance(OVERLOAD_BACKOFF_MAX_MS - 1)
  assert.equal(bridge.canCite, false)
  host.advance(1)
  assert.equal(bridge.canCite, true)
})

test('PR-Daemon #170 L1: a new document keeps the back-off, as the host keeps its own per view', () => {
  const { host, bridge } = opened()
  const a = bridge.cite(REF, CLICK)!
  host.reply(DOC_A, a.id, 'rejected', { code: 'unavailable', reason: 'overloaded', retry_after_ms: 1000 })
  host.hello(DOC_B)
  assert.equal(bridge.canCite, false)
  const before = host.sent.length
  host.advance(999)
  assert.equal(host.sent.length, before)
  host.advance(1)
  assert.equal(bridge.canCite, true)
  // The new document's status, held through the back-off, goes out now.
  assert.equal(host.sent.length, before + 1)
  assert.equal(host.sent.at(-1)!.document_id, DOC_B)
})

test('cancel: payload is { target_id }, needs no activation, and resolves with its own reply', async () => {
  const { host, bridge } = opened()
  const i = bridge.returnToChat([REF], CLICK)!
  host.active = false
  const c = bridge.cancel(i.id)
  const m = sentOf(host, 'cancel')
  assert.equal(m.length, 1)
  assert.deepEqual(m[0].payload, { target_id: i.id })
  assert.equal(m[0].document_id, DOC_A)
  host.reply(DOC_A, i.id, 'rejected', { code: 'cancelled' })
  host.reply(DOC_A, m[0].id as string, 'accepted')
  assert.deepEqual(await settled(i.reply), { result: 'rejected', code: 'cancelled' })
  // A cancel's accepted is its terminal reply (U7.2).
  assert.deepEqual(await settled(c), { result: 'accepted' })
  assert.equal(bridge.pendingCount, 0)
})

test('cancel sends nothing when closed or when the target is not a ULID', async () => {
  const { host, bridge } = started()
  const closed = bridge.cancel('01HZZZZZZZZZZZZZZZZZZZZZZZ')
  assert.equal(host.sent.length, 1)
  assert.equal(await settled(closed), null)
  host.hello(DOC_A)
  const before = host.sent.length
  for (const t of ['', 'x', '01hzzzzzzzzzzzzzzzzzzzzzzz', '01HZZZZZZZZZZZZZZZZZZZZZZZZ', '01HZZZZZZZZZZZZZZZZZZZZZZI']) {
    const p = bridge.cancel(t)
    assert.equal(host.sent.length, before, t)
    assert.equal(await settled(p), null, t)
  }
})

// ── Codex r1 ──────────────────────────────────────────────────────────────────

test('C1: a rate refusal without in_reply_to (⑥) still starts the back-off', () => {
  const { host, bridge } = opened()
  host.deliver({
    v: 3, direction: 'host_to_page', type: 'reply', id: ulid(host.clock, (b) => b.fill(3)), generation: 1,
    document_id: DOC_A, payload: { result: 'rejected', code: 'unavailable', reason: 'overloaded', retry_after_ms: 1000 },
  })
  assert.equal(bridge.canCite, false)
  assert.equal(bridge.cite(REF, CLICK), null)
  const before = host.sent.length
  bridge.setStatus({ state: 'ready' })
  assert.equal(host.sent.length, before)
  host.advance(1000)
  assert.equal(bridge.canCite, true)
  // The held status goes out once the back-off ends.
  assert.equal(host.sent.length, before + 1)
  assert.deepEqual(host.sent.at(-1)!.payload, { state: 'ready' })
})

test('C1: a rate refusal for another document does not back off this one', () => {
  const { host, bridge } = opened()
  host.deliver({
    v: 3, direction: 'host_to_page', type: 'reply', id: ulid(host.clock, (b) => b.fill(3)), generation: 1,
    document_id: DOC_B, payload: { result: 'rejected', code: 'unavailable', reason: 'overloaded', retry_after_ms: 1000 },
  })
  assert.equal(bridge.canCite, true)
})

test('C2: while IN_FLIGHT_MAX citations are pending a status is held; only the latest goes out when a slot frees', () => {
  const { host, bridge } = opened()
  const ids: string[] = []
  for (let n = 0; n < IN_FLIGHT_MAX; n++) ids.push(bridge.cite(ref(n + 1), CLICK)!.id)
  const before = host.sent.length
  bridge.setStatus({ state: 'loading' })
  bridge.setStatus({ state: 'ready' })
  assert.equal(host.sent.length, before)
  host.reply(DOC_A, ids[0], 'done')
  assert.equal(host.sent.length, before + 1)
  assert.equal(host.sent.at(-1)!.type, 'status')
  assert.deepEqual(host.sent.at(-1)!.payload, { state: 'ready' })
  host.reply(DOC_A, ids[1], 'done')
  assert.equal(host.sent.length, before + 1)
})

test('C2: a slot freed by a timeout also lets the held status out', () => {
  const { host, bridge } = opened()
  for (let n = 0; n < IN_FLIGHT_MAX; n++) bridge.cite(ref(n + 1), CLICK)
  const before = host.sent.length
  bridge.setStatus({ state: 'ready' })
  assert.equal(host.sent.length, before)
  host.advance(CITATION_TIMEOUT_MS)
  assert.equal(host.sent.length, before + 1)
  assert.equal(host.sent.at(-1)!.type, 'status')
})

test('r2 C1: a status the host refuses with ⑯ (its slot outlived our timeout) goes out again after the back-off', () => {
  const { host, bridge } = opened()
  for (let n = 0; n < IN_FLIGHT_MAX; n++) bridge.cite(ref(n + 1), CLICK)
  bridge.setStatus({ state: 'ready' })
  host.advance(CITATION_TIMEOUT_MS)
  const status = host.sent.at(-1)!
  assert.equal(status.type, 'status')
  // The host still holds the four, so it answers the status with ⑯.
  host.reply(DOC_A, status.id as string, 'rejected', { code: 'unavailable', reason: 'overloaded', retry_after_ms: 1000 })
  const before = host.sent.length
  host.advance(999)
  assert.equal(host.sent.length, before)
  host.advance(1)
  assert.equal(host.sent.length, before + 1)
  assert.equal(host.sent.at(-1)!.type, 'status')
  assert.deepEqual(host.sent.at(-1)!.payload, { state: 'ready' })
  host.reply(DOC_A, host.sent.at(-1)!.id as string, 'done')
  host.advance(10_000)
  assert.equal(host.sent.length, before + 1)
})

test('C2: inside a ⑯ back-off the status waits for it to end, then goes out once', () => {
  const { host, bridge } = opened()
  const a = bridge.cite(REF, CLICK)!
  host.reply(DOC_A, a.id, 'rejected', { code: 'unavailable', reason: 'overloaded', retry_after_ms: 1000 })
  const before = host.sent.length
  bridge.setStatus({ state: 'loading' })
  bridge.setStatus({ state: 'error', error_code: 'not_found' })
  host.advance(999)
  assert.equal(host.sent.length, before)
  host.advance(1)
  assert.equal(host.sent.length, before + 1)
  assert.deepEqual(host.sent.at(-1)!.payload, { state: 'error', error_code: 'not_found' })
  host.advance(5000)
  assert.equal(host.sent.length, before + 1)
})

test('C4: a message over BRIDGE_MAX_MESSAGE_BYTES is neither sent nor registered', () => {
  const { host, bridge } = opened()
  // `"` passes the id rules but costs two bytes once escaped.
  const fat = (n: number): CitationRef => {
    const v = '"'.repeat(120) + String(n).padStart(8, '0')
    return { knowledge_base_id: v, knowledge_id: v, chunk_id: v, source_revision: v, index_build_id: v, chunk_revision: n, content_tag: v }
  }
  const refs = Array.from({ length: INTENT_CITATIONS_MAX }, (_, i) => fat(i))
  assert.ok(new TextEncoder().encode(JSON.stringify({ citations: refs })).length > BRIDGE_MAX_MESSAGE_BYTES)
  const before = host.sent.length
  assert.equal(bridge.returnToChat(refs, CLICK), null)
  assert.equal(host.sent.length, before)
  assert.equal(bridge.pendingCount, 0)
  assert.equal(bridge.canReturnToChat, true)
  // One of them alone fits.
  assert.ok(bridge.cite(refs[0], CLICK))
  for (const m of host.sent) assert.ok(new TextEncoder().encode(JSON.stringify(m)).length <= BRIDGE_MAX_MESSAGE_BYTES)
})

test('a new document voids pending citations and intents, and frees their slots', async () => {
  const { host, bridge } = opened()
  const c = bridge.cite(REF, CLICK)!
  const i = bridge.returnToChat([REF], CLICK)!
  host.hello(DOC_B)
  assert.equal(await c.reply, null)
  assert.equal(await i.reply, null)
  assert.equal(bridge.canReturnToChat, true)
  const again = bridge.cite(REF, CLICK)!
  assert.equal(sentOf(host, 'citation').at(-1)!.document_id, DOC_B)
  assert.ok(again)
})

test('onOpenChange reports the bridge opening, once per change', () => {
  const { host, bridge } = started()
  const seen: boolean[] = []
  bridge.onOpenChange((o) => seen.push(o))
  host.hello(DOC_A)
  host.hello(DOC_B)
  host.hello(DOC_B, 2, [2])
  assert.deepEqual(seen, [true, false])
})

test('status still uses the 10-second timeout', async () => {
  const { host, bridge } = opened()
  const p = bridge.request('status', { state: 'ready' })
  host.advance(REPLY_TIMEOUT_MS)
  assert.equal(await p, null)
})
