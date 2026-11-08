// Agent24 fork product patch A24-KB-40 (WeKnora#135).
import assert from 'node:assert/strict'
import test from 'node:test'

import {
  AGENT24_WRITE_API_KEY_CAPABILITIES,
  preservedAPIKeyCapabilities,
  preservedCapabilitiesUseKnowledgeScope,
} from './agent24ApiKeyCapabilities'
import { TENANT_API_KEY_CAPABILITIES } from './apiKeyCapabilities'

test('a key issued with write capabilities keeps them through an edit of this page (Codex r1 C1)', () => {
  const kept = preservedAPIKeyCapabilities(
    ['retrieve', 'kb_candidate_propose', 'kb_candidate_approve', 'kb_candidate_propose'], TENANT_API_KEY_CAPABILITIES)
  assert.deepEqual(kept, ['kb_candidate_propose', 'kb_candidate_approve'])
  assert.equal(preservedCapabilitiesUseKnowledgeScope(kept), true)
  assert.deepEqual(preservedAPIKeyCapabilities(['retrieve', 'chat'], TENANT_API_KEY_CAPABILITIES), [])
  assert.deepEqual(preservedAPIKeyCapabilities(undefined, TENANT_API_KEY_CAPABILITIES), [])
  assert.deepEqual(preservedAPIKeyCapabilities(null, TENANT_API_KEY_CAPABILITIES), [])
  assert.equal(preservedCapabilitiesUseKnowledgeScope([]), false)
  assert.equal(preservedCapabilitiesUseKnowledgeScope(['something_else']), false)
})

test('the write capabilities are not in the editable table, and match the server values', () => {
  for (const c of AGENT24_WRITE_API_KEY_CAPABILITIES) {
    assert.equal((TENANT_API_KEY_CAPABILITIES as readonly string[]).includes(c), false, c)
  }
  assert.deepEqual([...AGENT24_WRITE_API_KEY_CAPABILITIES],
    ['kb_candidate_propose', 'kb_candidate_review', 'kb_candidate_approve', 'kb_entry_withdraw'])
})
