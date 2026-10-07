package vault

import (
	"fmt"
	"html"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// linkRe находит ссылки двух видов:
//   - вики-ссылки Obsidian: [[цель]], [[цель#заголовок|подпись]], ![[картинка|300]] (группы 1–2);
//   - markdown-ссылки: [текст](путь "заголовок"), ![alt](<путь с пробелами>) (группы 3–6).
var linkRe = regexp.MustCompile(
	`(!?)\[\[([^\[\]\n]+?)\]\]` +
		`|(!?)\[((?:[^\[\]\n]|\[[^\[\]\n]*\])*)\]` +
		`\(\s*(<[^<>\n]*>|[^\s()<>]*(?:\([^\s()<>]*\)[^\s()<>]*)*)((?:\s+(?:"[^"\n]*"|'[^'\n]*'))?)\s*\)`)

var (
	sizeRe   = regexp.MustCompile(`^(\d+)(?:x(\d+))?$`)
	schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
)

type rewriter struct {
	idx  *index
	note *entry
	res  *Result
	seen map[string]bool // уже учтённые нерешённые ссылки этой заметки
}

// rewrite переписывает ссылки в заметке. Front matter и код (блоки ``` / ~~~
// и `инлайн`) не трогаются.
func (r *rewriter) rewrite(src []byte) []byte {
	var b strings.Builder
	b.Grow(len(src))
	lines := strings.SplitAfter(string(src), "\n")
	frontMatter := strings.TrimRight(lines[0], "\r\n") == "---"
	fence := ""
	for i, line := range lines {
		text := strings.TrimRight(line, "\r\n")
		switch {
		case frontMatter:
			if i > 0 && (text == "---" || text == "...") {
				frontMatter = false
			}
		case fence != "":
			if closesFence(text, fence) {
				fence = ""
			}
		case openFence(text) != "":
			fence = openFence(text)
		default:
			line = mapOutsideCode(line, r.rewriteText)
		}
		b.WriteString(line)
	}
	return []byte(b.String())
}

func (r *rewriter) rewriteText(s string) string {
	matches := linkRe.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		group := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return s[m[2*i]:m[2*i+1]]
		}
		whole := s[m[0]:m[1]]
		b.WriteString(s[last:m[0]])
		if m[4] >= 0 {
			b.WriteString(r.wiki(group(1) == "!", group(2), whole))
		} else {
			b.WriteString(r.mdLink(group(3) == "!", group(4), group(5), group(6), whole))
		}
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func (r *rewriter) wiki(embed bool, inner, orig string) string {
	target, alias, _ := strings.Cut(inner, "|")
	target = strings.TrimSpace(strings.TrimSuffix(target, `\`)) // в таблицах Obsidian пишет [[цель\|подпись]]
	alias = strings.TrimSpace(alias)
	file, anchor, _ := strings.Cut(target, "#")
	file = strings.TrimSpace(file)

	if file == "" { // [[#Заголовок]] — ссылка внутри той же заметки
		frag := fragment(anchor)
		if frag == "" {
			return orig
		}
		text := alias
		if text == "" {
			text = lastHeading(anchor)
		}
		return "[" + escapeText(text) + "](" + frag + ")"
	}

	e := r.idx.resolve(file, r.note.rel)
	if e == nil {
		r.unresolved(orig)
		return orig
	}
	dest := r.relTo(e)

	if embed && e.kind == KindImage {
		alt := alias
		if m := sizeRe.FindStringSubmatch(alias); m != nil {
			return imgTag(dest, stem(e.rel), m[1], m[2])
		}
		if alt == "" {
			alt = stem(e.rel)
		}
		return "![" + escapeText(alt) + "](" + dest + ")"
	}

	// Встраивание заметок и прочих файлов GitHub не умеет, поэтому делаем ссылку.
	text := alias
	if text == "" {
		text = file
		if h := lastHeading(anchor); h != "" {
			text += " > " + h
		}
	}
	if e.kind == KindNote {
		dest += fragment(anchor)
	}
	return "[" + escapeText(text) + "](" + dest + ")"
}

func (r *rewriter) mdLink(image bool, text, dest, title, orig string) string {
	newText := text
	if !image {
		newText = r.rewriteText(text) // картинка внутри ссылки: [![](a.png)](b.md)
	}
	build := func(d string) string {
		bang := ""
		if image {
			bang = "!"
		}
		return bang + "[" + newText + "](" + d + title + ")"
	}
	keep := func() string {
		if newText == text {
			return orig
		}
		return build(dest)
	}

	raw := strings.TrimSuffix(strings.TrimPrefix(dest, "<"), ">")
	if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "//") || schemeRe.MatchString(raw) {
		return keep()
	}
	p, frag, _ := strings.Cut(raw, "#")
	if dec, err := url.PathUnescape(p); err == nil {
		p = dec
	}
	e := r.idx.resolve(p, r.note.rel)
	if e == nil {
		r.unresolved(orig)
		return keep()
	}
	d := r.relTo(e)
	if frag != "" && e.kind == KindNote {
		if dec, err := url.PathUnescape(frag); err == nil {
			frag = dec
		}
		d += fragment(frag)
	}
	return build(d)
}

func (r *rewriter) unresolved(link string) {
	if r.seen[link] {
		return
	}
	r.seen[link] = true
	r.res.Unresolved = append(r.res.Unresolved, Unresolved{Note: r.note.rel, Link: link})
}

// relTo возвращает путь от заметки до файла в репозитории, пригодный для markdown.
func (r *rewriter) relTo(e *entry) string {
	rel, err := filepath.Rel(filepath.FromSlash(path.Dir(r.note.out)), filepath.FromSlash(e.out))
	if err != nil {
		rel = e.out
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	for i, s := range segs {
		segs[i] = escapeSegment(s)
	}
	return strings.Join(segs, "/")
}

// escapeSegment кодирует пробелы, скобки и прочие символы, ломающие markdown-ссылку.
// Не-ASCII (кириллица) остаётся как есть: GitHub его понимает, а в исходнике так читаемее.
func escapeSegment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("-._~!$&*+,;=:@", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func imgTag(src, alt, width, height string) string {
	tag := `<img src="` + html.EscapeString(src) + `" alt="` + html.EscapeString(alt) + `" width="` + width + `"`
	if height != "" {
		tag += ` height="` + height + `"`
	}
	return tag + ">"
}

func escapeText(s string) string {
	return strings.NewReplacer(`[`, `\[`, `]`, `\]`).Replace(s)
}

func stem(p string) string {
	name := path.Base(p)
	return strings.TrimSuffix(name, path.Ext(name))
}

// fragment превращает якорь Obsidian («Заголовок», «Раздел#Подраздел») в якорь
// GitHub. Ссылки на блоки (^id) GitHub не поддерживает — остаётся ссылка на заметку.
func fragment(anchor string) string {
	h := lastHeading(anchor)
	if h == "" {
		return ""
	}
	return "#" + slug(h)
}

func lastHeading(anchor string) string {
	parts := strings.Split(anchor, "#")
	h := strings.TrimSpace(parts[len(parts)-1])
	if strings.HasPrefix(h, "^") {
		return ""
	}
	return h
}

// slug повторяет правило GitHub для якорей заголовков: нижний регистр,
// пробелы в дефисы, пунктуация удаляется.
func slug(heading string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case c == ' ':
			b.WriteByte('-')
		case c == '-' || unicode.IsLetter(c) || unicode.IsNumber(c) || unicode.IsMark(c) || unicode.Is(unicode.Pc, c):
			b.WriteRune(c)
		}
	}
	return b.String()
}

func openFence(line string) string {
	s := strings.TrimLeft(line, " \t>")
	if len(s) < 3 || s[0] != '`' && s[0] != '~' {
		return ""
	}
	n := runLen(s, s[0])
	if n < 3 || s[0] == '`' && strings.Contains(s[n:], "`") {
		return ""
	}
	return s[:n]
}

func closesFence(line, fence string) bool {
	s := strings.TrimLeft(line, " \t>")
	n := runLen(s, fence[0])
	return n >= len(fence) && strings.TrimSpace(s[n:]) == ""
}

// mapOutsideCode применяет fn к частям строки вне инлайн-кода (`...`, “...“).
func mapOutsideCode(s string, fn func(string) string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			b.WriteString(fn(s))
			return b.String()
		}
		n := runLen(s[i:], '`')
		end := closingRun(s[i+n:], n)
		if end < 0 { // незакрытые кавычки — обычный текст
			b.WriteString(fn(s[:i+n]))
			s = s[i+n:]
			continue
		}
		stop := i + n + end + n
		b.WriteString(fn(s[:i]))
		b.WriteString(s[i:stop])
		s = s[stop:]
	}
}

func closingRun(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		k := runLen(s[i:], '`')
		if k == n {
			return i
		}
		i += k
	}
	return -1
}

func runLen(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}
