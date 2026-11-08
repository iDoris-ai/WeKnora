import type { MessageKey } from '../i18n'

/** The label for a document parse_status as the UI entries return it (agent24CountedParseStatuses). */
export function statusKey(parseStatus: string): MessageKey {
  switch (parseStatus) {
    case 'completed':
    case 'finalizing':
      return 'status.ready'
    case 'failed':
      return 'status.failed'
    case 'cancelled':
      return 'status.cancelled'
    default:
      return 'status.building'
  }
}

/** A tag colour is used only when it is a plain hex colour. */
export function safeColor(color: string): string | undefined {
  return /^#[0-9a-fA-F]{3,8}$/.test(color) ? color : undefined
}

/**
 * What an empty page means: 'none' when the whole list is empty, 'beyond' when this page is past the end (the server
 * answers an out-of-range page with no items and the real total; Codex b-r1 C1), null when there are items.
 */
export function emptyKind(page: { items: unknown[]; total: number }): 'none' | 'beyond' | null {
  if (page.items.length > 0) return null
  return page.total > 0 ? 'beyond' : 'none'
}

/** A positive page number from a query value, 1 otherwise. */
export function pageParam(v: unknown): number {
  const n = typeof v === 'string' && /^[1-9]\d{0,5}$/.test(v) ? Number(v) : 1
  return n
}
