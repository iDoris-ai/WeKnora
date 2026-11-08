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
  'tab.documents': '文档',
  'tab.tags': '标签',
  'docs.empty': '这个知识库还没有文档',
  'docs.back': '返回文档列表',
  'docs.chunks': '{n} 个分块',
  'docs.revision': '源修订 {r}',
  'chunks.empty': '这个文档没有可显示的分块',
  'chunks.position': '#{n}',
  'tags.empty': '这个知识库没有标签',
  'tags.documents': '{n} 个文档',
  'status.building': '处理中',
  'status.failed': '处理失败',
  'status.cancelled': '已取消',
  'status.ready': '可检索',
  'pager.prev': '上一页',
  'pager.next': '下一页',
  'pager.of': '第 {p} / {n} 页',
  'pager.beyond': '这一页没有内容，请回到前一页',
  'md.image': '图片',
  'tab.wiki': 'Wiki',
  'wiki.empty': '这个知识库还没有已发布的 Wiki 页',
  'wiki.back': '返回 Wiki 页列表',
  'wiki.aliases': '别名：{a}',
  'wiki.version': '第 {v} 版',
  'wiki.links': '链接到',
  'wiki.backlinks': '被以下页面链接',
  'wiki.sources': '来源文档',
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
  'tab.documents': 'Documents',
  'tab.tags': 'Tags',
  'docs.empty': 'This knowledge base has no documents yet',
  'docs.back': 'Back to documents',
  'docs.chunks': '{n} chunks',
  'docs.revision': 'Source revision {r}',
  'chunks.empty': 'This document has no chunks to show',
  'chunks.position': '#{n}',
  'tags.empty': 'This knowledge base has no tags',
  'tags.documents': '{n} documents',
  'status.building': 'Processing',
  'status.failed': 'Failed',
  'status.cancelled': 'Cancelled',
  'status.ready': 'Searchable',
  'pager.prev': 'Previous',
  'pager.next': 'Next',
  'pager.of': 'Page {p} of {n}',
  'pager.beyond': 'Nothing on this page. Go back a page',
  'md.image': 'Image',
  'tab.wiki': 'Wiki',
  'wiki.empty': 'This knowledge base has no published wiki pages',
  'wiki.back': 'Back to wiki pages',
  'wiki.aliases': 'Also known as: {a}',
  'wiki.version': 'Version {v}',
  'wiki.links': 'Links to',
  'wiki.backlinks': 'Linked from',
  'wiki.sources': 'Source documents',
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
