// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). The page side of the bridge, design §9.
import assert from 'node:assert/strict'
import { test } from 'node:test'

import {
  BRIDGE_VERSION,
  HELLO_REQUESTS_MAX,
  HELLO_RETRY_MS,
  PageBridge,
  REPLY_TIMEOUT_MS,
  ulid,
  type BridgeHost,
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
  reply(doc: string, inReplyTo: string, result = 'done'): void {
    this.deliver({
      v: 3, direction: 'host_to_page', type: 'reply', id: ulid(this.clock, (b) => b.fill(2)), generation: 1,
      document_id: doc, in_reply_to: inReplyTo, payload: { result },
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
