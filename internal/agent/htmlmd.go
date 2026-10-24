package agent

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// htmlToMarkdown turns a web page into Markdown for the model: the main
// content (<main>, role=main or the largest <article>, else the body)
// without navigation, headers, footers, scripts, forms and cookie banners;
// headings, lists, tables, code blocks and links are kept.
func htmlToMarkdown(doc string, base *url.URL) (title, md string) {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return "", ""
	}
	r := &mdRenderer{base: base}
	title = strings.TrimSpace(collapseSpace(textOf(find(root, func(n *html.Node) bool { return isTag(n, "title") }))))
	body := find(root, func(n *html.Node) bool { return isTag(n, "body") })
	if body == nil {
		body = root
	}
	content := mainContent(body)
	if title == "" {
		title = strings.TrimSpace(collapseSpace(textOf(find(content, func(n *html.Node) bool { return isTag(n, "h1") }))))
	}
	var blocks []string
	r.container(content, &blocks)
	md = strings.Join(blocks, "\n\n")
	md = blankLines.ReplaceAllString(md, "\n\n")
	return title, strings.TrimSpace(md)
}

var blankLines = regexp.MustCompile(`\n{3,}`)

// mainContent picks the element holding the page's content: the largest
// main, role=main or article element, or a content element inside it that
// holds most of its text (an article inside a main with navigation).
func mainContent(body *html.Node) *html.Node {
	isContent := func(n *html.Node) bool {
		return n.Type == html.ElementNode && (n.Data == "main" || attr(n, "role") == "main" || n.Data == "article")
	}
	textLen := func(n *html.Node) int { return len(strings.TrimSpace(collapseSpace(textOf(n)))) }
	var best *html.Node
	bestLen := 0
	walk(body, func(n *html.Node) bool {
		if isContent(n) {
			if l := textLen(n); l > bestLen {
				best, bestLen = n, l
			}
		}
		return true
	})
	if best == nil || bestLen < 300 {
		return body
	}
	for {
		var inner *html.Node
		innerLen := 0
		walk(best, func(n *html.Node) bool {
			if n != best && isContent(n) {
				if l := textLen(n); l > innerLen {
					inner, innerLen = n, l
				}
			}
			return true
		})
		if inner == nil || innerLen*10 < bestLen*6 {
			return best
		}
		best, bestLen = inner, innerLen
	}
}

type mdRenderer struct{ base *url.URL }

var skipTags = map[string]bool{"script": true, "style": true, "noscript": true, "svg": true, "template": true, "iframe": true,
	"form": true, "button": true, "input": true, "select": true, "textarea": true, "nav": true, "footer": true, "aside": true,
	"canvas": true, "dialog": true, "head": true, "object": true, "embed": true, "video": true, "audio": true, "map": true}

var noiseClass = regexp.MustCompile(`(?i)(^|[\s_-])(sidebar|navbar|nav-bar|breadcrumbs?|cookies?|cookie-banner|toc|table-of-contents|site-header|site-footer|skip-link|advert|ads)([\s_-]|$)`)

func (r *mdRenderer) skip(n *html.Node) bool {
	switch n.Type {
	case html.CommentNode, html.DoctypeNode:
		return true
	case html.ElementNode:
	default:
		return false
	}
	if skipTags[n.Data] {
		return true
	}
	if n.Data == "header" && !insideContent(n) {
		return true
	}
	if _, hidden := attrOK(n, "hidden"); hidden || attr(n, "aria-hidden") == "true" {
		return true
	}
	switch attr(n, "role") {
	case "navigation", "banner", "contentinfo", "search", "dialog":
		return true
	}
	return noiseClass.MatchString(attr(n, "class")) || noiseClass.MatchString(attr(n, "id"))
}

// insideContent reports whether n is inside an article or main element (a
// header there is the article's own title block).
func insideContent(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && (p.Data == "article" || p.Data == "main") {
			return true
		}
	}
	return false
}

var blockTags = map[string]bool{"p": true, "div": true, "section": true, "article": true, "main": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "ul": true, "ol": true, "pre": true, "blockquote": true, "table": true, "hr": true, "dl": true,
	"figure": true, "header": true, "details": true, "summary": true, "li": true, "body": true, "center": true, "address": true}

func isBlock(n *html.Node) bool { return n.Type == html.ElementNode && blockTags[n.Data] }

// container renders n's children as blocks; inline runs become paragraphs.
func (r *mdRenderer) container(n *html.Node, out *[]string) {
	if n == nil {
		return
	}
	var para strings.Builder
	flush := func() {
		if t := tidyInline(para.String()); t != "" {
			*out = append(*out, t)
		}
		para.Reset()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r.skip(c) {
			continue
		}
		if isBlock(c) {
			flush()
			r.block(c, out)
		} else {
			para.WriteString(r.inline(c))
		}
	}
	flush()
}

func (r *mdRenderer) block(n *html.Node, out *[]string) {
	add := func(s string) {
		if s = strings.TrimRight(s, " \n"); strings.TrimSpace(s) != "" {
			*out = append(*out, s)
		}
	}
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		if t := tidyInline(r.inlineChildren(n)); t != "" {
			add(strings.Repeat("#", int(n.Data[1]-'0')) + " " + strings.ReplaceAll(t, "\n", " "))
		}
	case "p", "summary", "address":
		add(tidyInline(r.inlineChildren(n)))
	case "pre":
		code := strings.Trim(textOf(n), "\n")
		lang := codeLang(n)
		fence := "```"
		for strings.Contains(code, fence) {
			fence += "`"
		}
		add(fence + lang + "\n" + code + "\n" + fence)
	case "ul", "ol":
		add(r.list(n, 0))
	case "blockquote":
		var sub []string
		r.container(n, &sub)
		var lines []string
		for _, l := range strings.Split(strings.Join(sub, "\n\n"), "\n") {
			lines = append(lines, strings.TrimRight("> "+l, " "))
		}
		add(strings.Join(lines, "\n"))
	case "table":
		add(r.table(n))
	case "hr":
		add("---")
	case "dl":
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch {
			case isTag(c, "dt"):
				fmt.Fprintf(&b, "\n**%s**", tidyInline(r.inlineChildren(c)))
			case isTag(c, "dd"):
				fmt.Fprintf(&b, "\n: %s", tidyInline(r.inlineChildren(c)))
			}
		}
		add(strings.TrimSpace(b.String()))
	default:
		r.container(n, out)
	}
}

// codeLang finds the language of a code block from its classes.
func codeLang(pre *html.Node) string {
	for _, n := range []*html.Node{pre, find(pre, func(n *html.Node) bool { return isTag(n, "code") })} {
		if n == nil {
			continue
		}
		for _, c := range strings.Fields(attr(n, "class")) {
			for _, p := range []string{"language-", "lang-", "highlight-source-"} {
				if l, ok := strings.CutPrefix(c, p); ok && l != "" {
					return l
				}
			}
		}
		if l := attr(n, "data-lang"); l != "" {
			return l
		}
	}
	return ""
}

func (r *mdRenderer) list(n *html.Node, depth int) string {
	var lines []string
	i := 1
	if s := attr(n, "start"); s != "" {
		fmt.Sscanf(s, "%d", &i)
	}
	indent := strings.Repeat("  ", depth)
	for li := n.FirstChild; li != nil; li = li.NextSibling {
		if !isTag(li, "li") || r.skip(li) {
			continue
		}
		marker := "- "
		if n.Data == "ol" {
			marker = fmt.Sprintf("%d. ", i)
			i++
		}
		var parts []string
		var para strings.Builder
		flush := func() {
			if t := tidyInline(para.String()); t != "" {
				parts = append(parts, t)
			}
			para.Reset()
		}
		var nested []string
		for c := li.FirstChild; c != nil; c = c.NextSibling {
			switch {
			case r.skip(c):
			case isTag(c, "ul") || isTag(c, "ol"):
				flush()
				if l := r.list(c, depth+1); l != "" {
					nested = append(nested, l)
				}
			case isBlock(c):
				flush()
				r.block(c, &parts)
			default:
				para.WriteString(r.inline(c))
			}
		}
		flush()
		text := strings.Join(parts, "\n")
		if text == "" && len(nested) == 0 {
			continue
		}
		pad := indent + strings.Repeat(" ", len(marker))
		for k, l := range strings.Split(text, "\n") {
			if k == 0 {
				lines = append(lines, indent+marker+l)
			} else if l != "" {
				lines = append(lines, pad+l)
			}
		}
		lines = append(lines, nested...)
	}
	return strings.Join(lines, "\n")
}

func (r *mdRenderer) table(n *html.Node) string {
	var rows [][]string
	walk(n, func(c *html.Node) bool {
		if isTag(c, "table") && c != n {
			return false // nested tables are flattened into their cell
		}
		if !isTag(c, "tr") {
			return true
		}
		var row []string
		for cell := c.FirstChild; cell != nil; cell = cell.NextSibling {
			if isTag(cell, "td") || isTag(cell, "th") {
				t := tidyInline(r.inlineChildren(cell))
				t = strings.ReplaceAll(strings.ReplaceAll(t, "\n", " "), "|", `\|`)
				row = append(row, t)
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
		return false
	})
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, row := range rows {
		cols = max(cols, len(row))
	}
	var b strings.Builder
	for i, row := range rows {
		for len(row) < cols {
			row = append(row, "")
		}
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *mdRenderer) inlineChildren(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if !r.skip(c) {
			b.WriteString(r.inline(c))
		}
	}
	return b.String()
}

func (r *mdRenderer) inline(n *html.Node) string {
	if n.Type == html.TextNode {
		return collapseSpace(n.Data)
	}
	if n.Type != html.ElementNode {
		return ""
	}
	switch n.Data {
	case "br":
		return "\n"
	case "code", "kbd", "samp", "tt":
		t := strings.TrimSpace(collapseSpace(textOf(n)))
		if t == "" {
			return ""
		}
		if strings.Contains(t, "`") {
			return "`` " + t + " ``"
		}
		return "`" + t + "`"
	case "strong", "b":
		return wrapInline(r.inlineChildren(n), "**")
	case "em", "i":
		return wrapInline(r.inlineChildren(n), "*")
	case "a":
		text := strings.TrimSpace(r.inlineChildren(n))
		href := r.abs(attr(n, "href"))
		if text == "" {
			return ""
		}
		if href == "" || strings.HasPrefix(href, "#") {
			return text
		}
		return "[" + text + "](" + href + ")"
	case "img":
		// the model cannot see images: keep what the alt text says, but
		// not inside links (badges, logos)
		alt := strings.TrimSpace(collapseSpace(attr(n, "alt")))
		if alt == "" || insideLink(n) {
			return ""
		}
		return "[image: " + alt + "]"
	}
	if isBlock(n) {
		// a block inside inline content (e.g. a div in a table cell)
		return " " + r.inlineChildren(n) + " "
	}
	return r.inlineChildren(n)
}

func wrapInline(s, mark string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return s
	}
	lead := s[:len(s)-len(strings.TrimLeft(s, " "))]
	trail := s[len(strings.TrimRight(s, " ")):]
	return lead + mark + t + mark + trail
}

// abs resolves a link against the page; javascript: and data: links are dropped.
func (r *mdRenderer) abs(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	lower := strings.ToLower(href)
	if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "vbscript:") {
		return ""
	}
	if r.base == nil {
		return href
	}
	u, err := r.base.Parse(href)
	if err != nil {
		return href
	}
	return u.String()
}

var spaceRun = regexp.MustCompile(`[ \t\r\n\f\x{00a0}]+`)

func collapseSpace(s string) string { return spaceRun.ReplaceAllString(s, " ") }

var spaces = regexp.MustCompile(` {2,}`)

// tidyInline trims a paragraph's lines and the spaces between its elements.
func tidyInline(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		if l = strings.TrimSpace(spaces.ReplaceAllString(l, " ")); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func insideLink(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if isTag(p, "a") {
			return true
		}
	}
	return false
}

func textOf(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	walk(n, func(c *html.Node) bool {
		if c.Type == html.ElementNode && (c.Data == "script" || c.Data == "style") {
			return false
		}
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
		if isTag(c, "br") {
			b.WriteString("\n")
		}
		return true
	})
	return b.String()
}

// walk visits n and its descendants depth-first; fn returns false to skip
// a node's children.
func walk(n *html.Node, fn func(*html.Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func find(n *html.Node, pred func(*html.Node) bool) *html.Node {
	var found *html.Node
	walk(n, func(c *html.Node) bool {
		if found != nil {
			return false
		}
		if pred(c) {
			found = c
			return false
		}
		return true
	})
	return found
}

func isTag(n *html.Node, tag string) bool {
	return n != nil && n.Type == html.ElementNode && n.Data == tag
}

func attr(n *html.Node, key string) string {
	v, _ := attrOK(n, key)
	return v
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}
