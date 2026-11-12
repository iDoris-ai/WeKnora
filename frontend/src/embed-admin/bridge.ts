// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). The page side of the bridge, CONTRACT-UI-BRIDGE-v1
// §6/§7 at v 3 (Agent24#1660, decision #37), design §9.
// Page→host: a POST of the envelope to the reserved path; the host takes and cancels it, it never reaches the
// server. Host→page: a `message` event on window whose data is the envelope.
// Nothing here is a secret or a security decision (U6.1). document_id lives in this module only and goes into
// bridge envelopes only, never into a fetch to the API, a URL or storage.
// KB-C2-02d (same patch A24-KB-39; M2 verdict C item 2): citation, return_to_chat_intent and cancel (U6.2, U7.2), the part
// weknora-asks.md item 14 asked for and the KB-C2-02 slices left out. Payloads follow the host's reader,
// Agent24 apps/desktop/src/main/knowledge-bridge-message.ts (payloadOf, citationRef). The two content-carrying
// messages go out only from a trusted click while the page holds transient user activation; the host still
// applies its own five-second activation rule to the intent (U7.2), which this does not replace.

import type { StatusErrorCode } from './api'
import { isCitationRef, type CitationRef } from './cite'

export const BRIDGE_PATH = '/__agent24/bridge'
export const BRIDGE_VERSION = 3
/** Each request waiting for a reply gives up after this long, silently (PR-Daemon's note on #1660). */
export const REPLY_TIMEOUT_MS = 10_000
/** A hello request with no hello yet is sent again after this long, at most HELLO_REQUESTS_MAX in all (U6.3). */
export const HELLO_RETRY_MS = 1_000
export const HELLO_REQUESTS_MAX = 3
/** A citation waits through the bridge budget's queue (≤10 s) and one read; silently given up after this. */
export const CITATION_TIMEOUT_MS = 60_000
/** An intent waits for the user's confirmation (≤60 s, U7.2) and then the reads; silently given up after this. */
export const INTENT_TIMEOUT_MS = 120_000
/** U6.7: citation and intent messages in progress at the host, at most; cancel and status take no slot there. */
export const IN_FLIGHT_MAX = 4
/** Back-off after an `overloaded` reply that carries no retry_after_ms (the host sends 1000). */
export const OVERLOAD_BACKOFF_MS = 1_000
/** U7.2: an intent carries 0–8 citations. */
export const INTENT_CITATIONS_MAX = 8

export interface PageStatus {
  state: 'loading' | 'ready' | 'error'
  error_code?: StatusErrorCode
}

export interface HostState {
  profile_state: string
  code?: string
  theme: 'light' | 'dark'
  locale: string
}

export interface Reply {
  result: 'accepted' | 'rejected' | 'done'
  code?: string
  reason?: string
  retry_after_ms?: number
}

/** The event a click handler received. Only a trusted `click` may send citation or return_to_chat_intent. */
export interface UserTrigger {
  readonly isTrusted: boolean
  readonly type: string
}

/** A message that went out: its envelope id (what cancel targets) and its terminal reply, or null. */
export interface Sent {
  id: string
  reply: Promise<Reply | null>
}

export type { CitationRef }

interface Envelope {
  v: number
  direction: string
  type: string
  id: string
  generation: number
  document_id?: string
  in_reply_to?: string
  payload: Record<string, unknown>
}

/** What the bridge needs from the page environment. Tests give a fake one. */
export interface BridgeHost {
  send(body: string): void
  listen(onData: (data: unknown) => void): () => void
  setTimer(fn: () => void, ms: number): unknown
  clearTimer(handle: unknown): void
  now(): number
  random(bytes: Uint8Array<ArrayBuffer>): void
  /** True while the page holds transient user activation (navigator.userActivation.isActive); false without the API. */
  userActive(): boolean
}

const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'

/** A ULID (U7.1): 48-bit milliseconds, then 80 random bits. */
export function ulid(now: number, random: (bytes: Uint8Array<ArrayBuffer>) => void): string {
  let t = Math.max(0, Math.floor(now))
  let time = ''
  for (let i = 0; i < 10; i++) {
    time = CROCKFORD[t % 32] + time
    t = Math.floor(t / 32)
  }
  const bytes = new Uint8Array(16)
  random(bytes)
  let rest = ''
  for (const b of bytes) rest += CROCKFORD[b % 32]
  return time + rest
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function hostEnvelope(data: unknown): Envelope | null {
  if (!isObject(data) || !isObject(data.payload)) return null
  if (data.v !== BRIDGE_VERSION || data.direction !== 'host_to_page') return null
  if (typeof data.type !== 'string' || typeof data.id !== 'string') return null
  if (typeof data.generation !== 'number' || typeof data.document_id !== 'string') return null
  return data as unknown as Envelope
}

interface Pending {
  type: 'status' | 'citation' | 'return_to_chat_intent' | 'cancel'
  timer: unknown
  resolve: (reply: Reply | null) => void
}

export class PageBridge {
  private documentId: string | null = null
  private generation = 0
  private open = false
  private helloRequests = 0
  private helloTimer: unknown = null
  private status: PageStatus = { state: 'loading' }
  private readonly pending = new Map<string, Pending>()
  private readonly stateListeners = new Set<(s: HostState) => void>()
  private readonly openListeners = new Set<(open: boolean) => void>()
  private unlisten: (() => void) | null = null

  constructor(private readonly host: BridgeHost) {}

  /** Listens, then asks for hello. Until a hello arrives nothing else is sent. */
  start(): void {
    if (this.unlisten) return
    this.unlisten = this.host.listen((data) => this.onData(data))
    this.sendHelloRequest()
  }

  stop(): void {
    this.unlisten?.()
    this.unlisten = null
    if (this.helloTimer !== null) this.host.clearTimer(this.helloTimer)
    this.helloTimer = null
    this.dropPending()
  }

  /** True once a hello that supports this version has arrived. */
  get ready(): boolean {
    return this.open
  }

  /** Records the page status and reports it if the bridge is open. Not resent: a new hello sends the current one. */
  setStatus(status: PageStatus): void {
    this.status = status.state === 'error' ? { state: 'error', error_code: status.error_code ?? 'internal' } : { state: status.state }
    if (this.open) void this.request('status', { ...this.status })
  }

  onState(fn: (s: HostState) => void): () => void {
    this.stateListeners.add(fn)
    return () => this.stateListeners.delete(fn)
  }

  /** Called with the new value each time `ready` changes. */
  onOpenChange(fn: (open: boolean) => void): () => void {
    this.openListeners.add(fn)
    return () => this.openListeners.delete(fn)
  }

  /**
   * True when a citation could go out now: open, fewer than IN_FLIGHT_MAX citations and intents pending, and not
   * inside a back-off. The host answers a full table with `rejected`, code `unavailable`, reason `overloaded` and
   * `retry_after_ms` (1000), and counts a strike for anything sent inside that window (U6.6), so after such a reply
   * nothing goes out until now ≥ the reply time + retry_after_ms (OVERLOAD_BACKOFF_MS when the field is missing).
   */
  get canCite(): boolean {
    throw new Error('KB-C2-02d: not implemented')
  }

  /** True when an intent could go out now: canCite, and no intent pending (U6.7: at most one). */
  get canReturnToChat(): boolean {
    throw new Error('KB-C2-02d: not implemented')
  }

  /**
   * U7.2 citation. Sends `ref` as the payload itself (the seven fields, nothing else) and returns the id and the
   * terminal reply (`accepted` is not terminal; given up after CITATION_TIMEOUT_MS). Null, and nothing sent, when
   * the trigger is not a trusted click, the page holds no user activation, `ref` is not a valid reference, or
   * canCite is false.
   */
  cite(ref: CitationRef, trigger: UserTrigger): Sent | null {
    void ref
    void trigger
    throw new Error('KB-C2-02d: not implemented')
  }

  /**
   * U7.2 return_to_chat_intent with payload `{ citations }`. Same gate as cite, plus canReturnToChat and at most
   * INTENT_CITATIONS_MAX references, every one valid. Given up after INTENT_TIMEOUT_MS.
   */
  returnToChat(refs: readonly CitationRef[], trigger: UserTrigger): Sent | null {
    void refs
    void trigger
    throw new Error('KB-C2-02d: not implemented')
  }

  /**
   * U7.2 cancel with payload `{ target_id }`. Needs no user activation and takes no slot. Resolves with its own
   * reply, or null when the bridge is not open or `targetId` is not a ULID (then nothing is sent).
   */
  cancel(targetId: string): Promise<Reply | null> {
    void targetId
    throw new Error('KB-C2-02d: not implemented')
  }

  /** Number of requests still waiting for a reply. */
  get pendingCount(): number {
    return this.pending.size
  }

  /**
   * Sends one page→host message with the current document_id and generation. Resolves with the reply, or with
   * null when none comes within REPLY_TIMEOUT_MS or a new document's hello clears it. Never rejects.
   */
  request(type: 'status', payload: Record<string, unknown>): Promise<Reply | null> {
    return this.send(type, payload, REPLY_TIMEOUT_MS)?.reply ?? Promise.resolve(null)
  }

  /** The one place a page→host message with a document_id leaves. Null, and nothing sent, when not open. */
  private send(type: Pending['type'], payload: Record<string, unknown>, timeoutMs: number): Sent | null {
    void isCitationRef
    void type
    void payload
    void timeoutMs
    throw new Error('KB-C2-02d: not implemented (generalise the old request body below; keep its behaviour)')
  }

  private oldRequestBody(type: 'status', payload: Record<string, unknown>): Promise<Reply | null> {
    if (!this.open || this.documentId === null) return Promise.resolve(null)
    const id = ulid(this.host.now(), (b) => this.host.random(b))
    const env: Envelope = {
      v: BRIDGE_VERSION,
      direction: 'page_to_host',
      type,
      id,
      generation: this.generation,
      document_id: this.documentId,
      payload,
    }
    return new Promise((resolve) => {
      const timer = this.host.setTimer(() => {
        if (this.pending.delete(id)) resolve(null)
      }, REPLY_TIMEOUT_MS)
      this.pending.set(id, { timer, resolve })
      this.host.send(JSON.stringify(env))
    })
  }

  private sendHelloRequest(): void {
    if (this.open || this.helloRequests >= HELLO_REQUESTS_MAX) return
    this.helloRequests++
    // U6.3: the hello request is a generation-0 status. It carries no document_id and never `supported`.
    const env: Envelope = {
      v: BRIDGE_VERSION,
      direction: 'page_to_host',
      type: 'status',
      id: ulid(this.host.now(), (b) => this.host.random(b)),
      generation: 0,
      payload: { state: 'loading' },
    }
    this.host.send(JSON.stringify(env))
    this.helloTimer = this.host.setTimer(() => {
      this.helloTimer = null
      this.sendHelloRequest()
    }, HELLO_RETRY_MS)
  }

  private dropPending(): void {
    for (const p of this.pending.values()) {
      this.host.clearTimer(p.timer)
      p.resolve(null)
    }
    this.pending.clear()
  }

  private onData(data: unknown): void {
    const env = hostEnvelope(data)
    if (!env) return
    if (env.type === 'hello') {
      this.onHello(env)
      return
    }
    // Optional (design §9): a state or reply of another document is not this one's.
    if (env.document_id !== this.documentId) return
    if (env.type === 'state') {
      const p = env.payload
      if (typeof p.profile_state !== 'string' || typeof p.locale !== 'string') return
      if (p.theme !== 'light' && p.theme !== 'dark') return
      const s: HostState = { profile_state: p.profile_state, theme: p.theme, locale: p.locale }
      if (typeof p.code === 'string') s.code = p.code
      for (const fn of this.stateListeners) fn(s)
      return
    }
    if (env.type === 'reply' && typeof env.in_reply_to === 'string') {
      const pending = this.pending.get(env.in_reply_to)
      const result = env.payload.result
      if (!pending || (result !== 'accepted' && result !== 'rejected' && result !== 'done')) return
      // `accepted` is not terminal for citation and intent; status only ever gets a terminal reply.
      if (result === 'accepted') return
      this.pending.delete(env.in_reply_to)
      this.host.clearTimer(pending.timer)
      pending.resolve(env.payload as unknown as Reply)
    }
  }

  private onHello(env: Envelope): void {
    const supported = env.payload.supported
    if (!Array.isArray(supported) || env.generation < 1) return
    const docId = env.document_id as string
    const changed = docId !== this.documentId || env.generation !== this.generation
    // U6.3: the last hello wins. A new document voids every request of the old one; none will get a reply.
    if (docId !== this.documentId) this.dropPending()
    this.documentId = docId
    this.generation = env.generation
    if (this.helloTimer !== null) this.host.clearTimer(this.helloTimer)
    this.helloTimer = null
    const wasOpen = this.open
    this.open = supported.includes(BRIDGE_VERSION)
    if (this.open !== wasOpen) for (const fn of this.openListeners) fn(this.open)
    if (this.open && changed) void this.request('status', { ...this.status })
  }
}

/** The real page environment. */
export function browserBridgeHost(): BridgeHost {
  return {
    send(body) {
      // The host cancels this request, so the promise rejects; that is the normal outcome.
      fetch(BRIDGE_PATH, { method: 'POST', body, credentials: 'omit', cache: 'no-store' }).catch(() => {})
    },
    listen(onData) {
      // The host dispatches a synthetic MessageEvent, whose source is null; a posted message has a source.
      const h = (e: MessageEvent) => {
        if (e.source === null) onData(e.data)
      }
      window.addEventListener('message', h)
      return () => window.removeEventListener('message', h)
    },
    setTimer: (fn, ms) => window.setTimeout(fn, ms),
    clearTimer: (handle) => window.clearTimeout(handle as number),
    now: () => Date.now(),
    random: (bytes) => crypto.getRandomValues(bytes),
    userActive: () => {
      throw new Error('KB-C2-02d: not implemented')
    },
  }
}
