// Agent24 fork product patch A24-KB-43 (KB-SOP-02 S2, WeKnora#146, CONTRACT-OPS-v2 W8.2, W8.13): the content
// rules of a knowledge-entry candidate that no schema can express (W8.14): the restricted Markdown of the body,
// the title's whitespace as ECMAScript reads it, and the entry hash, scheme 1.

package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// The W8.2 limits: title 1–200 characters after trimming (it must already be trimmed, W8.13), body at most 64 KiB of
// UTF-8 bytes, kind as the §6 closed set reads it.
const (
	agent24EntryTitleMaxRunes = 200
	agent24EntryBodyMaxBytes  = 64 << 10
)

// agent24RestrictedMarkdownViolation reports whether body leaves the W8.2 restricted Markdown. Outside fenced code
// blocks and inline code spans it refuses:
//   - raw HTML: "<" followed by an ASCII letter, "/", "!" or "?" (an autolink "<https://…>" is raw HTML too);
//   - images: "![";
//   - footnotes: "[^";
//   - escape sequences other than HTML entities: a backslash before ASCII punctuation or at the end of a line;
//   - a link whose destination is not a plain https:// URL, inline "](dest)" or a reference definition
//     "[label]: dest" anywhere on a line (CommonMark takes definitions inside quotes and list items too, for the
//     whole document, and a label may span lines): no title, and no backtick, "<", ">", quote, backslash or
//     parenthesis in it; so any "]:", even inside inline code, must be followed by such a destination and nothing
//     else;
//   - the label of a full reference "[text][label]" not closed on its line, or with a backtick in it;
//   - a backtick run with no run of the same length after it on its line.
//
// Every rule errs on the side of refusing. What it skips as code must be code to CommonMark too, so a backtick never
// pairs across lines (CommonMark would pair it across a paragraph's lines), never inside a link destination (which
// CommonMark parses first), and a fence opens only at the start of a line with no indent (an indented fence can
// belong to a list item, whose end closes it); a closing fence may be indented up to three spaces, and an unclosed
// fence runs to the end. Anything else (fences in lists or quotes, indented code) is judged as prose. CommonMark reads
// a definition and the second label of a full reference as raw text, before any code span (Codex S2 r3:
// "[x`]: http://a \"`\"" defines a link, and "[go][x`]<img>`" then uses it, ending the "code" at the label), so
// agent24MarkdownRawViolation checks those on the raw line first.
func agent24RestrictedMarkdownViolation(body string) bool {
	fence := ""
	for _, line := range strings.Split(body, "\n") {
		if fence != "" {
			if agent24MarkdownClosesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if f, ok := agent24MarkdownOpensFence(line); ok { // no indent: see above
			fence = f
			continue
		}
		if agent24MarkdownRawViolation(line) || agent24MarkdownLineViolation(line) {
			return true
		}
	}
	return false
}

// agent24MarkdownFenceIndent strips at most three leading spaces; false if the line is indented further.
func agent24MarkdownFenceIndent(line string) (string, bool) {
	n := len(line) - len(strings.TrimLeft(line, " "))
	if n > 3 {
		return "", false
	}
	return line[n:], true
}

func agent24MarkdownOpensFence(rest string) (string, bool) {
	if rest == "" || (rest[0] != '`' && rest[0] != '~') {
		return "", false
	}
	run := len(rest) - len(strings.TrimLeft(rest, rest[:1]))
	if run < 3 {
		return "", false
	}
	if rest[0] == '`' && strings.Contains(rest[run:], "`") {
		return "", false // CommonMark: a backtick fence's info string has no backtick
	}
	return rest[:run], true
}

func agent24MarkdownClosesFence(line, fence string) bool {
	rest, ok := agent24MarkdownFenceIndent(line)
	if !ok {
		return false
	}
	run := len(rest) - len(strings.TrimLeft(rest, fence[:1]))
	return run >= len(fence) && strings.TrimRight(rest[run:], " \t") == ""
}

// agent24MarkdownRawViolation judges what CommonMark reads on a line before code spans: whatever follows any "]:"
// must be a safe destination, and the label after any "][" must close on the line with no backtick in it.
func agent24MarkdownRawViolation(line string) bool {
	for i := 0; i+1 < len(line); i++ {
		if line[i] != ']' {
			continue
		}
		switch line[i+1] {
		case ':':
			if !agent24MarkdownSafeDestination(strings.TrimLeft(line[i+2:], " \t")) {
				return true
			}
		case '[':
			end := strings.IndexByte(line[i+2:], ']')
			if end < 0 || strings.Contains(line[i+2:i+2+end], "`") {
				return true
			}
		}
	}
	return false
}

// agent24MarkdownLineViolation judges one line outside fenced code.
func agent24MarkdownLineViolation(line string) bool {
	for i := 0; i < len(line); i++ {
		switch b := line[i]; b {
		case '`':
			run := agent24MarkdownRun(line, i, '`')
			end := agent24MarkdownCodeSpanEnd(line, i+run, run)
			if end < 0 {
				return true // CommonMark could pair it with a run on a later line
			}
			i = end - 1
		case '\\':
			if i+1 == len(line) || agent24ASCIIPunct(line[i+1]) {
				return true
			}
		case '<':
			if i+1 < len(line) {
				n := line[i+1]
				if n == '/' || n == '!' || n == '?' || (n|0x20 >= 'a' && n|0x20 <= 'z') {
					return true
				}
			}
		case '!':
			if i+1 < len(line) && line[i+1] == '[' {
				return true
			}
		case '[':
			if i+1 < len(line) && line[i+1] == '^' {
				return true
			}
		case ']':
			// "]:" is agent24MarkdownRawViolation's; the scan goes on over its destination, since the line may be
			// prose after all ("a [x]: https://e.org![y]")
			if i+1 < len(line) && line[i+1] == '(' {
				rest := strings.TrimLeft(line[i+2:], " \t")
				end := strings.IndexAny(rest, " \t)")
				if end < 0 || rest[end] != ')' || !agent24MarkdownSafeDestination(rest[:end]) {
					return true
				}
				i = len(line) - len(rest) + end // the destination is never scanned for code spans
			}
		}
	}
	return false
}

// agent24MarkdownRun is the length of the run of c starting at i.
func agent24MarkdownRun(s string, i int, c byte) int {
	n := 0
	for i+n < len(s) && s[i+n] == c {
		n++
	}
	return n
}

// agent24MarkdownCodeSpanEnd finds the closing backtick run of exactly n backticks at or after from and returns
// the index just past it, or -1 when the line has none.
func agent24MarkdownCodeSpanEnd(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := agent24MarkdownRun(s, i, '`')
		if run == n {
			return i + run
		}
		i += run
	}
	return -1
}

// agent24MarkdownSafeDestination reports whether dest is a link destination the restricted Markdown keeps: an
// https:// URL with something after the scheme and none of the characters that could start another construct.
func agent24MarkdownSafeDestination(dest string) bool {
	return len(dest) > len("https://") && strings.EqualFold(dest[:len("https://")], "https://") &&
		!strings.ContainsAny(dest, " \t`<>\"'\\()")
}

func agent24ASCIIPunct(b byte) bool {
	return (b >= '!' && b <= '/') || (b >= ':' && b <= '@') || (b >= '[' && b <= '`') || (b >= '{' && b <= '~')
}

// agent24EntryTitleValid is the W8.2 title rule: 1–200 characters, first and last not ECMAScript whitespace (the
// schema's \s), no CR (checked at ②).
func agent24EntryTitleValid(title string) bool {
	n := utf8.RuneCountInString(title)
	if n == 0 || n > agent24EntryTitleMaxRunes {
		return false
	}
	first, _ := utf8.DecodeRuneInString(title)
	last, _ := utf8.DecodeLastRuneInString(title)
	return !agent24ECMAScriptSpace(first) && !agent24ECMAScriptSpace(last)
}

// agent24ECMAScriptSpace is ECMAScript's WhiteSpace or LineTerminator, what \s matches: unlike unicode.IsSpace it
// includes U+FEFF and excludes U+0085.
func agent24ECMAScriptSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// agent24EntryHash is W8.13 scheme 1: lowercase hex SHA-256 of the UTF-8 of ECMAScript
// JSON.stringify([1, kind, title, body]). The strings must be valid UTF-8 (the request decoder guarantees it).
func agent24EntryHash(kind, title, body string) string {
	var b strings.Builder
	b.WriteString("[1,")
	agent24ECMAScriptQuote(&b, kind)
	b.WriteByte(',')
	agent24ECMAScriptQuote(&b, title)
	b.WriteByte(',')
	agent24ECMAScriptQuote(&b, body)
	b.WriteByte(']')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// agent24ECMAScriptQuote writes s as ECMAScript JSON.stringify quotes a well-formed string: the short escapes for
// '"', '\\', \b, \f, \n, \r and \t, \u00xx (lowercase) for the other controls below U+0020, everything else as is
// (no escaping of '<', '>', '&', U+2028 or U+2029, which encoding/json would escape).
func agent24ECMAScriptQuote(b *strings.Builder, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0xF])
				continue
			}
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}
