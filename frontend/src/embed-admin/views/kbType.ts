import type { MessageKey } from '../i18n'

export function typeKey(type: string): MessageKey {
  switch (type) {
    case 'faq':
      return 'kb.type.faq'
    case 'wiki':
      return 'kb.type.wiki'
    default:
      return 'kb.type.document'
  }
}
