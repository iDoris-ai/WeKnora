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

/**
 * U7.1 reference ID, including the default refusal (decision #21): a string of 1–128 UTF-8 bytes, no control
 * character (U+0000–U+001F, U+007F–U+009F), and no `/`, `\`, `:`, `..` or whitespace.
 */
export function refIdOk(v: unknown): v is string {
  void v
  throw new Error('KB-C2-02d: not implemented')
}

/**
 * True only for an object with exactly the seven keys (no prototype tricks: own enumerable keys only), every ID
 * field passing refIdOk, and chunk_revision an integer in 0..2^31−1.
 */
export function isCitationRef(v: unknown): v is CitationRef {
  void v
  throw new Error('KB-C2-02d: not implemented')
}

/**
 * The reference for one chunk of a document: ids from doc.get and chunk.list. Null when index_build_id is null
 * or any field would fail isCitationRef; the view then shows neither action for that chunk. A new object with only
 * the seven keys, never the chunk or document object itself.
 */
export function citationRefOf(doc: DocumentDetail, chunk: Chunk): CitationRef | null {
  void doc
  void chunk
  throw new Error('KB-C2-02d: not implemented')
}

/**
 * The ticked list after the user toggles `ref` (matched by chunk_id). Removing always works; adding at `max`
 * returns the list unchanged. Order is the order of ticking. Never mutates `list`.
 */
export function toggleRef(list: readonly CitationRef[], ref: CitationRef, max: number): CitationRef[] {
  void list
  void ref
  void max
  throw new Error('KB-C2-02d: not implemented')
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
 * any other terminal reply; `code` is the reply's `reason` when present, else its `code`. `not_sent`: the bridge
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
 * A null reply (timeout, or a new document's hello) clears the pending id and sets the outcome to null.
 */
export class CiteActions {
  outcome: Outcome = null

  constructor(
    private readonly bridge: CiteBridge,
    private readonly onChange: () => void,
  ) {}

  /** True while an intent is waiting for its terminal reply. */
  get intentPending(): boolean {
    throw new Error('KB-C2-02d: not implemented')
  }

  /** Sends one citation from a click. `not_sent` when the bridge returns null. */
  cite(ref: CitationRef, trigger: Trigger): void {
    void ref
    void trigger
    throw new Error('KB-C2-02d: not implemented')
  }

  /** Sends the intent from a click; `waiting` until its terminal reply. */
  returnToChat(refs: readonly CitationRef[], trigger: Trigger): void {
    void refs
    void trigger
    throw new Error('KB-C2-02d: not implemented')
  }

  /** Cancels the waiting intent, if any. */
  cancelIntent(): void {
    throw new Error('KB-C2-02d: not implemented')
  }

  /** Cancels every citation and intent still waiting, once; later replies are ignored. */
  dispose(): void {
    throw new Error('KB-C2-02d: not implemented')
  }
}
