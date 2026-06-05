package gateway

import "strings"

// Helpers for Telegram MarkdownV2. The rules are finicky: in normal text a long list
// of punctuation must be backslash-escaped, while inside code spans only backslash and
// backtick matter. We keep dynamic/user-derived text in code spans where possible and
// run any prose through mdV2, so the controlled messages never trip a 400 from Telegram.

// mdV2SpecialChars must be escaped in MarkdownV2 normal text. See
// https://core.telegram.org/bots/api#markdownv2-style
const mdV2SpecialChars = "_*[]()~`>#+-=|{}.!\\"

// mdV2 escapes a plain string for use as MarkdownV2 normal text.
func mdV2(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if strings.ContainsRune(mdV2SpecialChars, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mdInline renders s as inline `code` (tap-to-copy in Telegram).
func mdInline(s string) string {
	return "`" + escapeCode(s) + "`"
}

// mdCodeBlock renders s as a fenced code block.
func mdCodeBlock(s string) string {
	return "```\n" + escapeCode(s) + "\n```"
}

// escapeCode escapes the only two characters that are special inside MarkdownV2 code.
func escapeCode(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}
