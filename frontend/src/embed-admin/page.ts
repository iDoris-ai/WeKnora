// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). One bridge per page, shared by the views.

import { ApiError } from './api'
import { browserBridgeHost, PageBridge, type PageStatus } from './bridge'
import type { MessageKey } from './i18n'

export const bridge = new PageBridge(browserBridgeHost())

/** The message a view shows for an error. */
export function errorMessage(err: unknown): MessageKey {
  const e = err instanceof ApiError ? err : new ApiError('internal', undefined, 0)
  if (e.code === 'forbidden' && e.reason === 'empty_scope') return 'error.empty_scope'
  return `error.${e.statusCode}` as MessageKey
}

export interface Load {
  /** False once a newer load has started: drop this load's result. */
  readonly current: boolean
  ready(): void
  /** Reports the error if this load is still current, and returns the message to show. */
  error(err: unknown): MessageKey
}

/**
 * Only the latest load reports to the bridge. A view starts a load when it shows something new; a request of an
 * earlier load that finishes later (the user went back, or opened another knowledge base) changes nothing.
 */
export class LoadTracker {
  private seq = 0

  constructor(private readonly sink: { setStatus(s: PageStatus): void }) {}

  begin(): Load {
    const token = ++this.seq
    const tracker = this
    this.sink.setStatus({ state: 'loading' })
    return {
      get current() {
        return token === tracker.seq
      },
      ready() {
        if (token === tracker.seq) tracker.sink.setStatus({ state: 'ready' })
      },
      error(err: unknown): MessageKey {
        const e = err instanceof ApiError ? err : new ApiError('internal', undefined, 0)
        if (token === tracker.seq) tracker.sink.setStatus({ state: 'error', error_code: e.statusCode })
        return errorMessage(err)
      },
    }
  }
}

export const loads = new LoadTracker(bridge)
