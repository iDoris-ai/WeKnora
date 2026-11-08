// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134, design §4). Markdown for chunk text, safe by construction:
// a private Marked instance (the global one is untouched) whose renderer escapes every piece of text and emits only
// the tags in SAFE_TAGS. Raw HTML is shown as text. A link is not navigable: its text, then its URL as text.
// An image is a text placeholder: nothing is fetched (no file proxy, no resource scheme, no remote URL, no data:).
// Code blocks carry no language class. Mermaid, KaTeX and highlighting are not in this version.
// The view still passes the result through DOMPurify with the same allowlist (MarkdownBlock.vue).
// KB-C2-02c: `[[slug]]` and `[[slug|text]]` in wiki content. A slug in the page's own out_links (published in this
// knowledge base) becomes <span class="ea-md-wiki" data-slug="…">, which the view turns into an in-page move; any
// other slug is plain text. Outside wiki content the brackets stay text.

import { Marked, Tokenizer, type Tokens } from 'marked'

/** Every tag the renderer can emit, and the only attributes: `class` with an ea-md- value, `start` on ol. */
export const SAFE_TAGS = [
  'p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'li', 'strong', 'em', 'del', 'code', 'pre',
  'blockquote', 'hr', 'br', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'span',
] as const
export const SAFE_ATTRS = ['class', 'start', 'data-slug'] as const

export function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

export interface MarkdownLabels {
  image: string
}

export interface MarkdownOptions {
  /** Wiki content: the slugs that may become links. Absent: `[[…]]` stays text. */
  wikiLinks?: ReadonlySet<string>
}

interface WikiLinkToken {
  type: 'wikilink'
  raw: string
  slug: string
  text: string
}

/** Set only for the duration of one synchronous parse. */
let wikiLinks: ReadonlySet<string> | null = null

// A GFM table splits its row on every unescaped `|` before inline extensions run, so `[[slug|text]]` in a cell would
// be cut in two (Codex c-r1 C1). In wiki content only, and only for the table tokenizer, every `|` inside `[[…]]`
// is replaced by BAR_MASK (one UTF-16 unit, so lengths and `raw` are unchanged) before the default table tokenizer
// runs; the wikilink tokenizer accepts it as the separator and the final HTML turns any left over back into `|`.
// Nothing outside tables is rewritten.
const BAR_MASK = '\uE000'
const WIKI_SPAN = /\[\[[^[\]\n]{1,512}\]\]/g

function maskWikiBars(src: string): string {
  return src.replace(WIKI_SPAN, (w) => w.replaceAll('|', BAR_MASK))
}

function build(labels: MarkdownLabels): Marked {
  const m = new Marked({ gfm: true, breaks: false, async: false })
  m.use({
    extensions: [
      {
        name: 'wikilink',
        level: 'inline',
        start(src: string) {
          const i = src.indexOf('[[')
          return i < 0 ? undefined : i
        },
        tokenizer(src: string): WikiLinkToken | undefined {
          const match = /^\[\[([^[\]\n]{1,512})\]\]/.exec(src)
          if (!match) return undefined
          const inner = match[1]
          const bar = /\\?[|\uE000]/.exec(inner)
          const slug = (bar && bar.index > 0 ? inner.slice(0, bar.index) : inner).trim()
          const text = (bar && bar.index > 0 ? inner.slice(bar.index + bar[0].length) : inner).trim() || slug
          return { type: 'wikilink', raw: match[0], slug, text }
        },
        renderer(token) {
          const t = token as unknown as WikiLinkToken
          if (!wikiLinks) return escapeHtml(t.raw)
          if (t.slug !== '' && wikiLinks.has(t.slug)) {
            return `<span class="ea-md-wiki" data-slug="${escapeHtml(t.slug)}">${escapeHtml(t.text)}</span>`
          }
          return `<span class="ea-md-nolink">${escapeHtml(t.text)}</span>`
        },
      },
    ],
    tokenizer: {
      table(this: Tokenizer, src: string): Tokens.Table | undefined | false {
        if (!wikiLinks || !src.includes('[[')) return false
        // A table ends at a blank line at the latest; only that much is masked.
        const end = src.search(/\n[ \t]*\n/)
        const head = end < 0 ? src : src.slice(0, end)
        return Tokenizer.prototype.table.call(this, maskWikiBars(head) + src.slice(head.length))
      },
    },
    hooks: {
      postprocess(html: string): string {
        return wikiLinks ? html.replaceAll(BAR_MASK, '|') : html
      },
    },
    renderer: {
      html(token: Tokens.HTML | Tokens.Tag): string {
        return escapeHtml(token.text)
      },
      link(token: Tokens.Link): string {
        const inner = this.parser.parseInline(token.tokens)
        return `<span class="ea-md-link">${inner}</span> <span class="ea-md-url">(${escapeHtml(token.href)})</span>`
      },
      image(token: Tokens.Image): string {
        const alt = token.text ? `: ${escapeHtml(token.text)}` : ''
        return `<span class="ea-md-img">[${escapeHtml(labels.image)}${alt}]</span>`
      },
      checkbox(token: Tokens.Checkbox): string {
        return token.checked ? '☑ ' : '☐ '
      },
      // Text is always escaped here. marked marks text after a raw-block tag (`<script>`, `<pre>` …) as
      // `escaped: true` while leaving it unescaped, so that flag is not trusted.
      text(token: Tokens.Text | Tokens.Escape): string {
        if ('tokens' in token && token.tokens) return this.parser.parseInline(token.tokens)
        return escapeHtml(token.text)
      },
      code(token: Tokens.Code): string {
        return `<pre><code>${escapeHtml(token.text.replace(/\n$/, '') + '\n')}</code></pre>\n`
      },
      tablecell(token: Tokens.TableCell): string {
        const tag = token.header ? 'th' : 'td'
        return `<${tag}>${this.parser.parseInline(token.tokens)}</${tag}>\n`
      },
    },
  })
  return m
}

const cache = new Map<string, Marked>()

/** HTML for one piece of markdown. Never throws: on a parser error the text is shown escaped. */
export function renderMarkdown(text: string, labels: MarkdownLabels, opts: MarkdownOptions = {}): string {
  const key = labels.image
  let m = cache.get(key)
  if (!m) {
    m = build(labels)
    cache.set(key, m)
  }
  wikiLinks = opts.wikiLinks ?? null
  try {
    return m.parse(text) as string
  } catch {
    return `<p>${escapeHtml(text)}</p>`
  } finally {
    wikiLinks = null
  }
}
