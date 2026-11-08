// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). Codex r1 C2: only the latest load reports to the bridge.
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { ApiError } from './api.ts'
import type { PageStatus } from './bridge.ts'
import { errorMessage, LoadTracker } from './page.ts'

function tracker(): { t: LoadTracker; seen: PageStatus[] } {
  const seen: PageStatus[] = []
  return { t: new LoadTracker({ setStatus: (s) => seen.push(s) }), seen }
}

test('a load reports loading, then ready or error', () => {
  const { t, seen } = tracker()
  t.begin().ready()
  const key = t.begin().error(new ApiError('unauthorized', 'credential_expired', 401))
  assert.equal(key, 'error.unauthorized')
  assert.deepEqual(seen, [
    { state: 'loading' }, { state: 'ready' },
    { state: 'loading' }, { state: 'error', error_code: 'unauthorized' },
  ])
})

test('a stale load that finishes later reports nothing and is not current', () => {
  const { t, seen } = tracker()
  const detail = t.begin() // the detail page starts loading
  const list = t.begin() // the user went back; the list loads and succeeds
  list.ready()
  assert.equal(detail.current, false)
  assert.equal(list.current, true)
  detail.error(new ApiError('unavailable', undefined, 503)) // the detail request fails afterwards
  detail.ready()
  assert.deepEqual(seen, [{ state: 'loading' }, { state: 'loading' }, { state: 'ready' }])
})

test('error messages: empty_scope has its own, anything else maps by status code', () => {
  assert.equal(errorMessage(new ApiError('forbidden', 'empty_scope', 403)), 'error.empty_scope')
  assert.equal(errorMessage(new ApiError('forbidden', 'operation_not_allowed', 403)), 'error.forbidden')
  assert.equal(errorMessage(new ApiError('timeout', undefined, 504)), 'error.unavailable')
  assert.equal(errorMessage(new Error('x')), 'error.internal')
})
