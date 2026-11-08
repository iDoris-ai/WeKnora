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

/** A positive page number from a query value, 1 otherwise. */
export function pageParam(v: unknown): number {
  const n = typeof v === 'string' && /^[1-9]\d{0,5}$/.test(v) ? Number(v) : 1
  return n
}
