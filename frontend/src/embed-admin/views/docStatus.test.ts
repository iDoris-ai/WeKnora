// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134).
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { pageParam, safeColor, statusKey } from './docStatus.ts'

test('parse_status labels', () => {
  assert.equal(statusKey('completed'), 'status.ready')
  assert.equal(statusKey('finalizing'), 'status.ready')
  assert.equal(statusKey('failed'), 'status.failed')
  assert.equal(statusKey('cancelled'), 'status.cancelled')
  assert.equal(statusKey('pending'), 'status.building')
  assert.equal(statusKey('processing'), 'status.building')
  assert.equal(statusKey('anything'), 'status.building')
})

test('only plain hex colours are used', () => {
  for (const ok of ['#fff', '#A1B2C3', '#a1b2c3d4']) assert.equal(safeColor(ok), ok)
  for (const bad of ['', 'red', '#ff', '#gggggg', 'url(x)', '#fff;background:url(x)', 'expression(1)', '#123456789']) {
    assert.equal(safeColor(bad), undefined, bad)
  }
})

test('page query values', () => {
  assert.equal(pageParam('3'), 3)
  for (const bad of [undefined, '', '0', '-1', '1.5', 'x', '01', '9999999', ['2']]) assert.equal(pageParam(bad), 1, String(bad))
})
