// Agent24 fork product patch A24-KB-40 (WeKnora#135, Codex cred-v2 r1 C1): the Agent24 write capabilities are
// issued through the API and are not in the editable capability table. Editing a key in this page must keep
// them (and the knowledge-base scope they need) instead of dropping them on save.

export const AGENT24_WRITE_API_KEY_CAPABILITIES: readonly string[] = [
  'kb_candidate_propose', 'kb_candidate_review', 'kb_candidate_approve', 'kb_entry_withdraw',
]

/** The capabilities of a key that the page cannot show: kept as they are, in their order, once each. */
export function preservedAPIKeyCapabilities(
  current: readonly string[] | null | undefined,
  editable: readonly string[],
): string[] {
  const shown = new Set(editable)
  const out: string[] = []
  for (const c of current ?? []) {
    if (!shown.has(c) && !out.includes(c)) out.push(c)
  }
  return out
}

/** Whether kept capabilities need the key's knowledge-base allow-list: the Agent24 write classes are scoped by it. */
export function preservedCapabilitiesUseKnowledgeScope(preserved: readonly string[]): boolean {
  return preserved.some((c) => AGENT24_WRITE_API_KEY_CAPABILITIES.includes(c))
}
