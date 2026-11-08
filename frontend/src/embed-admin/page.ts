// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). One bridge per page, shared by the views.

import { ApiError } from './api'
import { browserBridgeHost, PageBridge, type PageStatus } from './bridge'
import type { MessageKey } from './i18n'

export const bridge = new PageBridge(browserBridgeHost())

/** The message a view shows for an error, and the status the page reports. Nothing is retried. */
export function reportError(err: unknown): MessageKey {
  const e = err instanceof ApiError ? err : new ApiError('internal', undefined, 0)
  const status: PageStatus = { state: 'error', error_code: e.statusCode }
  bridge.setStatus(status)
  if (e.code === 'forbidden' && e.reason === 'empty_scope') return 'error.empty_scope'
  return `error.${e.statusCode}` as MessageKey
}

export function reportReady(): void {
  bridge.setStatus({ state: 'ready' })
}

export function reportLoading(): void {
  bridge.setStatus({ state: 'loading' })
}
