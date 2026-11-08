// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134). The UI read entries this page calls.
// Every template here must be a path in docs/agent24/ui-routes.json, method GET: the host injects the session
// only for that exact list (design §9). routes.test.ts checks this and that no other /api/ path appears.

export const UI_ROUTES = {
  kbList: '/api/v1/agent24/knowledge/ui/knowledge-bases',
  kbGet: '/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id',
} as const

export type UIRouteTemplate = (typeof UI_ROUTES)[keyof typeof UI_ROUTES]

/** Fills `:name` segments, each value encoded as one segment. A missing or empty value throws. */
export function uiPath(template: UIRouteTemplate, params: Record<string, string> = {}): string {
  return template.replace(/:([a-z_]+)/g, (_m, name: string) => {
    const v = params[name]
    if (typeof v !== 'string' || v === '') throw new Error(`missing route parameter ${name}`)
    return encodeURIComponent(v)
  })
}
