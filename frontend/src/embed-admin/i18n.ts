// Agent24 fork A24-KB-39 (KB-C2-02a, WeKnora#134, design §4). The page's own two locales.
// A small table instead of vue-i18n: no runtime message compiler, nothing for the CSP to forbid.

import { ref } from 'vue'

const zhCN = {
  'title.list': '知识库',
  'list.empty': '当前凭据看不到任何知识库',
  'list.documents': '{n} 个文档',
  'list.updated': '更新于 {d}',
  'kb.wiki': 'Wiki',
  'kb.type.document': '文档库',
  'kb.type.faq': '问答库',
  'kb.type.wiki': 'Wiki 库',
  'detail.back': '返回知识库列表',
  'detail.created': '创建于 {d}',
  'state.loading': '加载中…',
  'error.unauthorized': '会话已失效，请在 Agent24 中重新打开知识页',
  'error.forbidden': '没有查看权限',
  'error.empty_scope': '当前凭据看不到任何知识库',
  'error.not_found': '不存在或无权查看',
  'error.unavailable': '服务暂不可用，请稍后再试',
  'error.internal': '出错了，请稍后再试',
} as const

export type MessageKey = keyof typeof zhCN

const enUS: Record<MessageKey, string> = {
  'title.list': 'Knowledge bases',
  'list.empty': 'These credentials cannot see any knowledge base',
  'list.documents': '{n} documents',
  'list.updated': 'Updated {d}',
  'kb.wiki': 'Wiki',
  'kb.type.document': 'Documents',
  'kb.type.faq': 'FAQ',
  'kb.type.wiki': 'Wiki',
  'detail.back': 'Back to knowledge bases',
  'detail.created': 'Created {d}',
  'state.loading': 'Loading…',
  'error.unauthorized': 'The session has ended. Reopen the knowledge page in Agent24',
  'error.forbidden': 'You do not have access',
  'error.empty_scope': 'These credentials cannot see any knowledge base',
  'error.not_found': 'Not found, or you do not have access',
  'error.unavailable': 'The service is unavailable. Try again later',
  'error.internal': 'Something went wrong. Try again later',
}

export const MESSAGES: Record<'zh-CN' | 'en-US', Record<MessageKey, string>> = { 'zh-CN': zhCN, 'en-US': enUS }

export type Locale = keyof typeof MESSAGES

/** zh for any Chinese tag, en otherwise. */
export function pickLocale(tag: string | undefined): Locale {
  return tag && tag.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en-US'
}

export const locale = ref<Locale>(pickLocale(typeof navigator === 'undefined' ? undefined : navigator.language))

export function msg(key: MessageKey, vars: Record<string, string | number> = {}): string {
  return MESSAGES[locale.value][key].replace(/\{(\w+)\}/g, (m, name: string) =>
    name in vars ? String(vars[name]) : m,
  )
}

export function formatDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium' }).format(d)
}
