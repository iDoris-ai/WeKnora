// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134, design §4). The page's own HTTP client.
// No token, tenant header or storage: the host injects the session into these same-origin requests (U4.5).
// An error is never retried here and never redirects; the caller reports it over the bridge (U6.2).

import { UI_ROUTES, uiPath, type UIRouteTemplate } from './routes'

/** U7.2 status error codes. */
export type StatusErrorCode = 'unauthorized' | 'forbidden' | 'not_found' | 'unavailable' | 'internal'

export class ApiError extends Error {
  constructor(
    readonly code: string,
    readonly reason: string | undefined,
    readonly status: number,
  ) {
    super(reason ? `${code}/${reason}` : code)
  }

  /** The status error code the bridge carries for this error. */
  get statusCode(): StatusErrorCode {
    switch (this.code) {
      case 'unauthorized':
      case 'forbidden':
      case 'not_found':
      case 'unavailable':
        return this.code
      case 'timeout':
        return 'unavailable'
      default:
        return 'internal'
    }
  }
}

export type FetchLike = (input: string, init: RequestInit) => Promise<Response>

let fetchImpl: FetchLike = (input, init) => fetch(input, init)

/** Tests replace the transport. */
export function setFetch(f: FetchLike): void {
  fetchImpl = f
}

function errorFrom(status: number, body: unknown): ApiError {
  const err = (body as { error?: { code?: unknown; details?: { reason?: unknown } } } | null)?.error
  const code = typeof err?.code === 'string' ? err.code : status === 401 ? 'unauthorized' : 'internal'
  const reason = typeof err?.details?.reason === 'string' ? err.details.reason : undefined
  return new ApiError(code, reason, status)
}

export async function getJSON<T>(
  template: UIRouteTemplate,
  params: Record<string, string> = {},
  query: Record<string, string | number> = {},
): Promise<T> {
  let url = uiPath(template, params)
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(query)) qs.set(k, String(v))
  if ([...qs.keys()].length > 0) url += `?${qs.toString()}`
  let res: Response
  try {
    res = await fetchImpl(url, {
      method: 'GET',
      credentials: 'omit',
      cache: 'no-store',
      redirect: 'error',
      headers: { Accept: 'application/json' },
    })
  } catch {
    throw new ApiError('unavailable', undefined, 0)
  }
  let body: unknown = null
  try {
    body = await res.json()
  } catch {
    body = null
  }
  if (!res.ok) throw errorFrom(res.status, body)
  if (body === null || typeof body !== 'object') throw new ApiError('internal', undefined, res.status)
  return body as T
}

export interface KnowledgeBase {
  id: string
  name: string
  description: string
  type: string
  document_count: number
  wiki_enabled: boolean
  created_at: string
  updated_at: string
}

export async function listKnowledgeBases(): Promise<KnowledgeBase[]> {
  const body = await getJSON<{ knowledge_bases?: KnowledgeBase[] }>(UI_ROUTES.kbList)
  return Array.isArray(body.knowledge_bases) ? body.knowledge_bases : []
}

export async function getKnowledgeBase(kbId: string): Promise<KnowledgeBase> {
  const body = await getJSON<{ knowledge_base?: KnowledgeBase }>(UI_ROUTES.kbGet, { kb_id: kbId })
  if (!body.knowledge_base) throw new ApiError('internal', undefined, 200)
  return body.knowledge_base
}

// KB-C2-02b: documents, chunks and tags.

export interface Page<T> {
  items: T[]
  total: number
  page: number
  pageSize: number
}

function pageOf<T>(items: unknown, body: { total?: unknown; page?: unknown; page_size?: unknown }): Page<T> {
  const num = (v: unknown, d: number) => (typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : d)
  return {
    items: Array.isArray(items) ? (items as T[]) : [],
    total: num(body.total, 0),
    page: num(body.page, 1),
    pageSize: num(body.page_size, 1) || 1,
  }
}

export interface DocumentItem {
  knowledge_id: string
  knowledge_base_id: string
  title: string
  file_name: string
  parse_status: string
  created_at: string
  updated_at: string
}

export interface Revision {
  source_revision: string
  index_build_id: string | null
}

export interface DocumentDetail {
  knowledge_id: string
  knowledge_base_id: string
  title: string
  file_name: string
  parse_status: string
  revision: Revision
  chunk_count: number
}

export interface Chunk {
  chunk_id: string
  position: number
  chunk_revision: number
  content_tag: string
  text: string
}

export interface Tag {
  id: string
  name: string
  color: string
  document_count: number
}

export async function listDocuments(kbId: string, page: number): Promise<Page<DocumentItem>> {
  const body = await getJSON<{ documents?: unknown; total?: unknown; page?: unknown; page_size?: unknown }>(
    UI_ROUTES.docList, { kb_id: kbId }, { page })
  return pageOf<DocumentItem>(body.documents, body)
}

export async function getDocument(knowledgeId: string): Promise<DocumentDetail> {
  const body = await getJSON<{ document?: DocumentDetail }>(UI_ROUTES.docGet, { knowledge_id: knowledgeId })
  if (!body.document) throw new ApiError('internal', undefined, 200)
  return body.document
}

export async function listChunks(knowledgeId: string, page: number): Promise<Page<Chunk>> {
  const body = await getJSON<{ chunks?: unknown; total?: unknown; page?: unknown; page_size?: unknown }>(
    UI_ROUTES.chunkList, { knowledge_id: knowledgeId }, { page })
  return pageOf<Chunk>(body.chunks, body)
}

export async function listTags(kbId: string, page: number): Promise<Page<Tag>> {
  const body = await getJSON<{ tags?: unknown; total?: unknown; page?: unknown; page_size?: unknown }>(
    UI_ROUTES.tagList, { kb_id: kbId }, { page })
  return pageOf<Tag>(body.tags, body)
}
