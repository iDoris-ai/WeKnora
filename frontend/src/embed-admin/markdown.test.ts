// Agent24 fork A24-KB-39 (KB-C2-02b, WeKnora#134, design §4). The renderer emits only SAFE_TAGS with SAFE_ATTRS,
// never navigates, never fetches, and shows raw HTML as text, for hostile input and random mixes of it.
import assert from 'node:assert/strict'
import { test } from 'node:test'

import { escapeHtml, renderMarkdown, SAFE_ATTRS, SAFE_TAGS } from './markdown.ts'

const LABELS = { image: 'IMG' }
const TAGS = new Set<string>(SAFE_TAGS)
const ATTRS = new Set<string>(SAFE_ATTRS)

/** Every tag is in SAFE_TAGS; the only attributes are class="ea-md-…" and start="digits". */
function assertSafe(html: string, input: string): void {
  const where = JSON.stringify(input)
  for (const m of html.matchAll(/<\/?([a-zA-Z][a-zA-Z0-9-]*)([^>]*)>/g)) {
    const [, tag, rest] = m
    assert.ok(TAGS.has(tag.toLowerCase()), `${where}: tag <${tag}> in ${html}`)
    const attrs = rest.trim()
    if (attrs === '' || attrs === '/') continue
    for (const a of attrs.matchAll(/([^\s=]+)(?:="([^"]*)")?/g)) {
      const [, name, value] = a
      assert.ok(ATTRS.has(name), `${where}: attribute ${name} in ${html}`)
      if (name === 'class') assert.match(value ?? '', /^ea-md-[a-z]+$/, where)
      if (name === 'data-slug') assert.match(m[0], /^<span class="ea-md-wiki" data-slug="[^"]*">$/, where)
      if (name === 'start') {
        assert.equal(tag, 'ol', where)
        assert.match(value ?? '', /^\d{1,9}$/, where)
      }
    }
  }
  // A "<" that does not open a SAFE tag is always escaped, so no other element can appear.
  assert.equal(/<(?!\/?(?:p|h[1-6]|ul|ol|li|strong|em|del|code|pre|blockquote|hr|br|table|thead|tbody|tr|th|td|span)[\s>/])/.test(html), false, `${where}: ${html}`)
}

const HOSTILE = [
  '<script>alert(1)</script>',
  '<img src=x onerror=alert(1)>',
  '<a href="http://e">e</a>',
  '<iframe src="http://e"></iframe>',
  '<div style="background:url(x)">x</div>',
  'a & b <b onclick="x">bold</b> `x<y`',
  '[t](javascript:alert(1))',
  '[t](http://e "title\\" onmouseover=\\"x")',
  '<https://e.com> and https://bare.example',
  '![alt "q"](https://x/y.png)',
  '![](/files?file_path=a)',
  '![x](resource://abc)',
  '![x](data:image/png;base64,AAAA)',
  '[![i](a.png)](http://b)',
  '- [x] done\n- [ ] todo',
  '| a | b |\n|:-|-:|\n| <i>1</i> | 2 |',
  '```js" onload="x\ncode <tag>\n```',
  '```\n</code></pre><script>x</script>\n```',
  '    indented <b>code</b>',
  '3. three\n4. four',
  '<div>\nblock html\n</div>',
  '<!-- comment --><style>p{}</style>',
  '<svg><script>x</script></svg>',
  '<math><mi xlink:href="javascript:x">x</mi></math>',
  '[ref]\n\n[ref]: javascript:alert(1) "t"',
  '**[x](y)** ~~d~~ *e* > q',
  '# H1 {#id onclick=x}\n## h2\n###### h6',
  '> quote <b>x</b>\n> > nested',
  'line\\\nbreak  \nhard',
  '---\n***',
  'emoji 😀 and 中文 &amp; &lt;b&gt;',
  '`` `<script>` ``',
  '\u0000<scr\u0000ipt>',
  '<<script>script>alert(1)<</script>/script>',
  '<a\nhref="x">nl</a>',
  'foo<span class="evil">x</span>',
  '<script>`<b>` **<i>x</i>** [a](b) ![c](d)',
  '<pre>\n<b>x</b>\n</pre> after <i>y</i>',
  'a &amp; b &lt;c&gt; &#60;d&#62; \\<e\\>',
]

test('hostile markdown renders only safe tags and attributes', () => {
  for (const input of HOSTILE) assertSafe(renderMarkdown(input, LABELS), input)
})

test('raw HTML is shown as text, links are text plus URL, images are placeholders', () => {
  assert.equal(renderMarkdown('<script>alert(1)</script>', LABELS), '&lt;script&gt;alert(1)&lt;/script&gt;')
  const link = renderMarkdown('[t](javascript:alert(1))', LABELS)
  assert.match(link, /<span class="ea-md-link">t<\/span> <span class="ea-md-url">\(javascript:alert\(1\)\)<\/span>/)
  for (const src of ['https://x/y.png', '/files?file_path=a', 'resource://abc', 'data:image/png;base64,AAAA', 'a.png']) {
    const out = renderMarkdown(`![cap](${src})`, LABELS)
    assert.match(out, /<span class="ea-md-img">\[IMG: cap\]<\/span>/)
    assert.equal(out.includes(src), false, src)
  }
  // The URL is shown exactly as written, escaped, so an entity in it is not decoded on screen.
  assert.match(renderMarkdown('[t](http://a?x=1&lt;y)', LABELS), /\(http:\/\/a\?x=1&amp;lt;y\)/)
  assert.equal(renderMarkdown('- [x] a', LABELS).includes('<input'), false)
  assert.equal(renderMarkdown('```js\nx\n```', LABELS), '<pre><code>x\n</code></pre>\n')
  assert.equal(renderMarkdown('| a |\n|:-:|\n| b |', LABELS).includes('align'), false)
})

test('ordinary markdown still renders', () => {
  assert.equal(renderMarkdown('# T\n\n**b** *i* `c`', LABELS), '<h1>T</h1>\n<p><strong>b</strong> <em>i</em> <code>c</code></p>\n')
  assert.match(renderMarkdown('1. a\n2. b', LABELS), /^<ol>\n<li>a<\/li>/)
})

test('random mixes of hostile fragments stay safe', () => {
  let seed = 20261109
  const rand = () => {
    seed = (seed * 1103515245 + 12345) % 2147483648
    return seed / 2147483648
  }
  const parts = [...HOSTILE, '\n', '\n\n', ' ', '*', '_', '[', ']', '(', ')', '!', '`', '<', '>', '"', '|', '- ', '> ', '#']
  for (let i = 0; i < 2000; i++) {
    let s = ''
    const n = 1 + Math.floor(rand() * 8)
    for (let j = 0; j < n; j++) s += parts[Math.floor(rand() * parts.length)]
    assertSafe(renderMarkdown(s, LABELS), s)
  }
})

test('escapeHtml escapes the five characters', () => {
  assert.equal(escapeHtml(`<a href="x" title='y'>&</a>`), '&lt;a href=&quot;x&quot; title=&#39;y&#39;&gt;&amp;&lt;/a&gt;')
})

// KB-C2-02c: wiki links.
test('wiki links: only slugs in the set become links, everything else is text', () => {
  const set = new Set(['entity/acme-corp', 'b"><script>'])
  const out = renderMarkdown('see [[entity/acme-corp|Acme]] and [[missing]] and [[b"><script>]] [[ |x]]', LABELS, { wikiLinks: set })
  assert.match(out, /<span class="ea-md-wiki" data-slug="entity\/acme-corp">Acme<\/span>/)
  assert.match(out, /<span class="ea-md-nolink">missing<\/span>/)
  assert.match(out, /<span class="ea-md-wiki" data-slug="b&quot;&gt;&lt;script&gt;">b&quot;&gt;&lt;script&gt;<\/span>/)
  assert.match(out, /<span class="ea-md-nolink">x<\/span>/)
  assertSafe(out, 'wiki links')
})

test('outside wiki content the brackets stay text', () => {
  assert.equal(renderMarkdown('[[a|b]]', LABELS), '<p>[[a|b]]</p>\n')
})

test('the wiki link set does not leak into the next render', () => {
  renderMarkdown('[[a]]', LABELS, { wikiLinks: new Set(['a']) })
  assert.equal(renderMarkdown('[[a]]', LABELS), '<p>[[a]]</p>\n')
})

test('a wiki link with text inside a table cell stays one link (Codex c-r1 C1)', () => {
  const set = new Set(['entity/acme-corp'])
  const out = renderMarkdown('| 页面 | 说明 |\n| --- | --- |\n| [[entity/acme-corp|Acme]] | 重要说明 |\n| [[other|别处]] | x |\n', LABELS, {
    wikiLinks: set,
  })
  assert.match(out, /<td><span class="ea-md-wiki" data-slug="entity\/acme-corp">Acme<\/span><\/td>\n<td>重要说明<\/td>/)
  assert.match(out, /<td><span class="ea-md-nolink">别处<\/span><\/td>\n<td>x<\/td>/)
  assertSafe(out, 'wiki table')
})

test('code and raw HTML in wiki content keep the bar as written', () => {
  const set = new Set(['a'])
  assert.equal(renderMarkdown('`[[a|b]]`', LABELS, { wikiLinks: set }), '<p><code>[[a|b]]</code></p>\n')
  assert.equal(renderMarkdown('```\n[[a|b]]\n```', LABELS, { wikiLinks: set }), '<pre><code>[[a|b]]\n</code></pre>\n')
  assert.equal(renderMarkdown('<div>[[a|b]]</div>', LABELS, { wikiLinks: set }), '&lt;div&gt;[[a|b]]&lt;/div&gt;')
  assert.equal(renderMarkdown('`[[a|b]]`', LABELS), '<p><code>[[a|b]]</code></p>\n')
})

test('table cells: an escaped bar, several bars, and an unknown slug (Codex c-r2 C1, C2)', () => {
  const set = new Set(['a'])
  const row = (cell: string) => renderMarkdown(`| h | i |\n| - | - |\n| ${cell} | tail |\n`, LABELS, { wikiLinks: set })
  assert.match(row('[[a\\|b]]'), /<td><span class="ea-md-wiki" data-slug="a">b<\/span><\/td>\n<td>tail<\/td>/)
  assert.match(row('[[a|b|c]]'), /<td><span class="ea-md-wiki" data-slug="a">b\|c<\/span><\/td>\n<td>tail<\/td>/)
  assert.match(row('`[[a|b]]`'), /<td><code>\[\[a\|b\]\]<\/code><\/td>\n<td>tail<\/td>/)
  assert.match(row('x [[a|b]] y [[z|w]]'), /<td>x <span class="ea-md-wiki" data-slug="a">b<\/span> y <span class="ea-md-nolink">w<\/span><\/td>/)
  assert.equal(row('[[a|b]]').includes('\uE000'), false)
  // A row after the table and plain cells are untouched.
  assert.match(renderMarkdown('| h |\n| - |\n| a|b |\n', LABELS, { wikiLinks: set }), /<td>a<\/td>/)
})

test('outside tables nothing is rewritten (Codex c-r2 C3, C4, C5)', () => {
  const set = new Set(['a', 'x'])
  const md = (t: string) => renderMarkdown(t, LABELS, { wikiLinks: set })
  assert.match(md('<https://e.test/[[x|y]]>'), /\(https:\/\/e\.test\/\[\[x\|y\]\]\)/)
  assert.equal(md('<https://e.test/[[x|y]]>').includes('\\'), false)
  assert.equal(md('![`[[x|y]]`](i.png)').includes('\\'), false)
  assert.equal(md('`[[a\n\\|b]]`'), '<p><code>[[a \\|b]]</code></p>\n')
  const long = 'b'.repeat(510)
  assert.equal(md(`[[a|${long}]]`), `<p><span class="ea-md-wiki" data-slug="a">${long}</span></p>\n`)
  assert.equal(md('[[a\\|b]]'), '<p><span class="ea-md-wiki" data-slug="a">b</span></p>\n')
  assert.equal(renderMarkdown('| h |\n| - |\n| \uE000 |\n', LABELS).includes('\uE000'), true)
  // Outside wiki content a table is plain GFM: the bar splits the cell as written.
  assert.match(renderMarkdown('| h | i |\n| - | - |\n| [[a|b]] | tail |\n', LABELS), /<td>\[\[a<\/td>\n<td>b\]\]<\/td>/)
})

test('hostile wiki content stays safe', () => {
  const set = new Set(['a', 'x y', '<b>'])
  for (const input of [...HOSTILE, '[[a|<img src=x onerror=1>]]', '[[nope|<img src=x onerror=1>]]', '[[<b>]]', '[[<b>|t]]', '**[[a]]** [l]([[a]])', '[[a]]]]', '[[[[a]]]]']) {
    assertSafe(renderMarkdown(input, LABELS, { wikiLinks: set }), input)
  }
})
