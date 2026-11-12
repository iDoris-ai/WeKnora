// Agent24 fork A24-KB-39 (KB-C2-02d, WeKnora#134; M2 verdict C item 2). Citation references for the bridge
// and the document view's two actions: 「引用」 on a chunk, and 「回到对话提问」 with the chunks the user ticked.
// The reference rules mirror the host's reader (Agent24 apps/desktop/src/main/knowledge-bridge-message.ts,
// refId and citationRef; CONTRACT-UI-BRIDGE-v1 U7.1). The page never sends a reference the host would refuse:
// that would only cost a reply and, for a malformed one, count as a violation (U6.6).

import type { Chunk, DocumentDetail } from './api'

/** U7.2: the seven fields, exactly. */
export interface CitationRef {
  knowledge_base_id: string
  knowledge_id: string
  chunk_id: string
  source_revision: string
  index_build_id: string
  chunk_revision: number
  content_tag: string
}

/** The six string fields, in the host's order. */
export const REF_ID_FIELDS = [
  'knowledge_base_id',
  'knowledge_id',
  'chunk_id',
  'source_revision',
  'index_build_id',
  'content_tag',
] as const

const CITATION_KEYS = [...REF_ID_FIELDS, 'chunk_revision'].sort()

/**
 * U7.1 reference ID, including the default refusal (decision #21): a string of 1–128 UTF-8 bytes, no control
 * character (U+0000–U+001F, U+007F–U+009F), and no `/`, `\`, `:`, `..` or whitespace. No lone surrogate either:
 * TextEncoder would count it as U+FFFD, but JSON.stringify sends it as `\udXXX`, which the host's strict parser
 * refuses as a soft violation (Codex r1 C3).
 */
export function refIdOk(v: unknown): v is string {
  if (typeof v !== 'string') return false
  const n = new TextEncoder().encode(v).length
  if (n < 1 || n > 128) return false
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(v)) return false
  if (/[/\\:\s]|\.\./.test(v)) return false
  if (/[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/.test(v)) return false
  return true
}

/**
 * True only for a plain object (its prototype is Object.prototype) whose own enumerable keys are exactly the
 * seven, every ID field passing refIdOk, and chunk_revision an integer in 0..2^31−1.
 */
export function isCitationRef(v: unknown): v is CitationRef {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return false
  if (Object.getPrototypeOf(v) !== Object.prototype) return false
  const keys = Object.keys(v).sort()
  if (keys.length !== 7 || keys.some((k, i) => k !== CITATION_KEYS[i])) return false
  const o = v as Record<string, unknown>
  for (const field of REF_ID_FIELDS) {
    if (!refIdOk(o[field])) return false
  }
  const rev = o.chunk_revision
  return Number.isInteger(rev) && (rev as number) >= 0 && (rev as number) <= 2 ** 31 - 1
}

/**
 * The reference for one chunk of a document: ids from doc.get and chunk.list. Null when index_build_id is null
 * or any field would fail isCitationRef; the view then shows neither action for that chunk. A new object with only
 * the seven keys, never the chunk or document object itself.
 */
export function citationRefOf(doc: DocumentDetail, chunk: Chunk): CitationRef | null {
  if (doc.revision.index_build_id === null) return null
  const ref: CitationRef = {
    knowledge_base_id: doc.knowledge_base_id,
    knowledge_id: doc.knowledge_id,
    chunk_id: chunk.chunk_id,
    source_revision: doc.revision.source_revision,
    index_build_id: doc.revision.index_build_id,
    chunk_revision: chunk.chunk_revision,
    content_tag: chunk.content_tag,
  }
  return isCitationRef(ref) ? ref : null
}

/**
 * The ticked list after the user toggles `ref` (matched by chunk_id). Removing always works; adding at `max`
 * returns the list unchanged. Order is the order of ticking. Never mutates `list`.
 */
export function toggleRef(list: readonly CitationRef[], ref: CitationRef, max: number): CitationRef[] {
  if (list.some((r) => r.chunk_id === ref.chunk_id)) return list.filter((r) => r.chunk_id !== ref.chunk_id)
  if (list.length >= max) return [...list]
  return [...list, ref]
}

// ── the view's actions ────────────────────────────────────────────────────────

/** A trusted click, as the bridge checks it. */
export interface Trigger {
  readonly isTrusted: boolean
  readonly type: string
}

export interface ActionReply {
  result: 'accepted' | 'rejected' | 'done'
  code?: string
  reason?: string
}

/** The part of PageBridge the actions use. Tests give a fake one. */
export interface CiteBridge {
  cite(ref: CitationRef, trigger: Trigger): { id: string; reply: Promise<ActionReply | null> } | null
  returnToChat(refs: readonly CitationRef[], trigger: Trigger): { id: string; reply: Promise<ActionReply | null> } | null
  cancel(targetId: string): Promise<ActionReply | null>
}

/**
 * What the view shows next to the actions. `sent`: the host ended the message with `done` and no code. `failed`:
 * any other terminal reply except `rejected/cancelled`, which only answers the user's own cancel and shows no line
 * (PR-Daemon #170 L2; replies after dispose() are ignored anyway); `code` is the reply's `reason` when present, else
 * its `code`. `not_sent`: the bridge
 * refused to send (closed, busy, no activation). `waiting`: an intent is waiting for the user in the chat window.
 */
export type Outcome =
  | { kind: 'sent' }
  | { kind: 'failed'; code: string }
  | { kind: 'not_sent' }
  | { kind: 'waiting' }
  | null

/**
 * The document view's citation actions. Keeps the ids still waiting, so a cancel can target the intent and
 * dispose() can withdraw everything when the view goes away. A reply that arrives after dispose() changes nothing.
 * A null reply (timeout, or a new document's hello) clears the pending id and sets the outcome to null; the id is
 * kept as unanswered (the last UNANSWERED_KEPT), because the host may still be working on it and dispose() should
 * still withdraw it (Codex r1 C5). A cancel for an id the host has finished is answered `accepted` with no effect.
 */
/** How many timed-out ids dispose() still withdraws. */
export const UNANSWERED_KEPT = 8

export class CiteActions {
  outcome: Outcome = null
  private pending = new Map<string, 'cite' | 'intent'>()
  private readonly unanswered: string[] = []
  private disposed = false

  constructor(
    private readonly bridge: CiteBridge,
    private readonly onChange: () => void,
  ) {}

  /** True while an intent is waiting for its terminal reply. */
  get intentPending(): boolean {
    for (const kind of this.pending.values()) if (kind === 'intent') return true
    return false
  }

  /** Sends one citation from a click. `not_sent` when the bridge returns null. */
  cite(ref: CitationRef, trigger: Trigger): void {
    if (this.disposed) return
    const s = this.bridge.cite(ref, trigger)
    if (s === null) {
      this.outcome = { kind: 'not_sent' }
      this.onChange()
      return
    }
    this.pending.set(s.id, 'cite')
    this.onChange()
    void s.reply.then((r) => this.settle(s.id, r))
  }

  /** Sends the intent from a click; `waiting` until its terminal reply. */
  returnToChat(refs: readonly CitationRef[], trigger: Trigger): void {
    if (this.disposed) return
    const s = this.bridge.returnToChat(refs, trigger)
    if (s === null) {
      this.outcome = { kind: 'not_sent' }
      this.onChange()
      return
    }
    this.pending.set(s.id, 'intent')
    this.outcome = { kind: 'waiting' }
    this.onChange()
    void s.reply.then((r) => this.settle(s.id, r))
  }

  private settle(id: string, r: ActionReply | null): void {
    // dispose() empties pending, so a reply after it ends here.
    if (!this.pending.delete(id)) return
    if (r === null) {
      this.unanswered.push(id)
      if (this.unanswered.length > UNANSWERED_KEPT) this.unanswered.shift()
      this.outcome = null
    }
    else if (r.result === 'rejected' && r.code === 'cancelled') this.outcome = null
    else if (r.result === 'done' && r.code === undefined) this.outcome = { kind: 'sent' }
    else this.outcome = { kind: 'failed', code: r.reason ?? r.code ?? r.result }
    this.onChange()
  }

  /** Cancels the waiting intent, if any. */
  cancelIntent(): void {
    if (this.disposed) return
    for (const [id, kind] of this.pending) {
      if (kind === 'intent') {
        void this.bridge.cancel(id)
        return
      }
    }
  }

  /** Cancels every citation and intent still waiting, once; later replies are ignored. */
  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    for (const id of [...this.pending.keys(), ...this.unanswered]) void this.bridge.cancel(id)
    this.pending.clear()
    this.unanswered.length = 0
  }
}
