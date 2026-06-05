package gateway

import (
	"regexp"
	"strconv"
	"strings"
)

// Models speak GitHub-flavored Markdown (**bold**, `code`, - lists), but Telegram's
// MarkdownV2 is a different, fussy dialect. Rather than fight the model, we convert its
// Markdown to Telegram HTML, which only requires escaping & < > and tolerates the rest.
// The conversion is pragmatic, not a full CommonMark parser: it covers what the model
// actually emits, and anything it can't map is left as readable text.

var (
	reFence      = regexp.MustCompile("(?s)```[ \\t]*[\\w+-]*\\n?(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`\n]+)`")
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reHeading    = regexp.MustCompile(`^\s*#{1,6}\s+(.*?)\s*$`)
	reBullet     = regexp.MustCompile(`^(\s*)[-*+]\s+`)
	reBold       = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reBoldU      = regexp.MustCompile(`__([^_\n]+)__`)
	reItalic     = regexp.MustCompile(`\*([^*\n]+)\*`)
	reItalicU    = regexp.MustCompile(`_([^_\n]+)_`)
)

// mdToHTML converts a model's Markdown reply into Telegram-safe HTML.
func mdToHTML(src string) string {
	var stash []string
	keep := func(html string) string {
		tok := "\x00" + strconv.Itoa(len(stash)) + "\x00"
		stash = append(stash, html)
		return tok
	}

	// 1. Pull out code (fenced, then inline) so their contents are never reformatted.
	src = reFence.ReplaceAllStringFunc(src, func(m string) string {
		code := strings.TrimSuffix(reFence.FindStringSubmatch(m)[1], "\n")
		return keep("<pre>" + htmlEscape(code) + "</pre>")
	})
	src = reInlineCode.ReplaceAllStringFunc(src, func(m string) string {
		return keep("<code>" + htmlEscape(reInlineCode.FindStringSubmatch(m)[1]) + "</code>")
	})

	// 2. Pull out links (their URLs must not be touched by emphasis rules).
	src = reLink.ReplaceAllStringFunc(src, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		return keep(`<a href="` + htmlEscape(sub[2]) + `">` + htmlEscape(sub[1]) + `</a>`)
	})

	// 3. Escape everything that's left, then map block + inline formatting.
	src = htmlEscape(src)

	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		if m := reHeading.FindStringSubmatch(ln); m != nil {
			lines[i] = "<b>" + m[1] + "</b>"
			continue
		}
		lines[i] = reBullet.ReplaceAllString(ln, "$1• ") // bullets before emphasis eats leading *
	}
	src = strings.Join(lines, "\n")

	src = reBold.ReplaceAllString(src, "<b>$1</b>")
	src = reBoldU.ReplaceAllString(src, "<b>$1</b>")
	src = reItalic.ReplaceAllString(src, "<i>$1</i>")
	src = reItalicU.ReplaceAllString(src, "<i>$1</i>")

	// 4. Restore the stashed code/link HTML.
	for i := len(stash) - 1; i >= 0; i-- {
		src = strings.ReplaceAll(src, "\x00"+strconv.Itoa(i)+"\x00", stash[i])
	}
	return src
}

// htmlEscape escapes the three characters Telegram HTML treats specially.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
