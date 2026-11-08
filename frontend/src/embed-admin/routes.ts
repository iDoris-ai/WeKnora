// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). The UI read entries this page calls.
// Every template here must be a path in docs/agent24/ui-routes.json, method GET: the host injects the session
// only for that exact list (design §9). routes.test.ts checks this and that no other /api/ path appears.

export const UI_ROUTES = {
  kbList: '/api/v1/agent24/knowledge/ui/knowledge-bases',
  kbGet: '/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id',
  tagList: '/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id/tags',
  docList: '/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id/documents',
  docGet: '/api/v1/agent24/knowledge/ui/documents/:knowledge_id',
  chunkList: '/api/v1/agent24/knowledge/ui/documents/:knowledge_id/chunks',
  wikiList: '/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id/wiki/pages',
  wikiGet: '/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id/wiki/pages/*slug',
} as const

export type UIRouteTemplate = (typeof UI_ROUTES)[keyof typeof UI_ROUTES]

export class RouteParamError extends Error {}

/** A dot segment would be removed by URL parsing (`%2E` too), so the request would reach another entry. */
function segment(v: string, name: string): string {
  if (v === '' || v === '.' || v === '..') throw new RouteParamError(`bad route parameter ${name}`)
  return encodeURIComponent(v)
}

/**
 * Fills `:name` with one encoded segment and a final `*name` with a slash-separated value, each part encoded on its
 * own and the slashes kept (design §9: the host injects no session for `%2F`, the wiki root or an empty slug).
 * An empty value, an empty part, and a `.` or `..` part throw RouteParamError: nothing is requested.
 */
export function uiPath(template: UIRouteTemplate, params: Record<string, string> = {}): string {
  return template.replace(/([:*])([a-z_]+)/g, (_m, kind: string, name: string) => {
    const v = params[name]
    if (typeof v !== 'string') throw new RouteParamError(`missing route parameter ${name}`)
    if (kind === ':') return segment(v, name)
    return v.split('/').map((part) => segment(part, name)).join('/')
  })
}
