package gateway

import "strings"

// mdToANSI renders a Markdown reply as ANSI text for the terminal chat: bold/italic, inline
// `code` and fenced code blocks, headers, bullet & quote lines, links and rules. When
// colour is off (piped / NO_COLOR) every style helper is a no-op, so it degrades to clean
// plain text with the markup characters removed.
func mdToANSI(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	inCode := false
	for _, ln := range lines {
		t := strings.TrimRight(ln, " \t")
		tr := strings.TrimSpace(t)

		if strings.HasPrefix(tr, "```") {
			inCode = !inCode
			out = append(out, "") // a blank line frames the code block
			continue
		}
		if inCode {
			out = append(out, tdim("│ ")+tcode(t))
			continue
		}

		switch {
		case tr == "":
			out = append(out, "")
		case strings.HasPrefix(tr, "#"):
			out = append(out, tbold(tcol(colHead, strings.TrimSpace(strings.TrimLeft(tr, "# ")))))
		case tr == "---" || tr == "***" || tr == "___":
			out = append(out, tdim(strings.Repeat("─", 32)))
		case strings.HasPrefix(tr, "> "):
			out = append(out, tdim("│ ")+renderInline(strings.TrimPrefix(tr, "> ")))
		default:
			if rest, ok := bulletRest(tr); ok {
				out = append(out, tcol(colReply, "•")+" "+renderInline(rest))
				continue
			}
			out = append(out, renderInline(t))
		}
	}
	return strings.Join(collapseBlanks(out), "\n")
}

// bulletRest reports whether a line is a bullet item and returns its content.
func bulletRest(s string) (string, bool) {
	for _, p := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(s, p) {
			return s[len(p):], true
		}
	}
	return "", false
}

// renderInline applies inline markdown: **bold**, __bold__, *italic*, _italic_, `code`, and
// [text](url). It recurses into bold/link content so styles nest. Unterminated markers are
// left as literal characters.
func renderInline(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == '`':
			if j := mdIndex(r, i+1, "`"); j >= 0 {
				b.WriteString(tcode(string(r[i+1 : j])))
				i = j + 1
				continue
			}
		case mdAt(r, i, "**"):
			if j := mdIndex(r, i+2, "**"); j >= 0 {
				b.WriteString(tbold(renderInline(string(r[i+2 : j]))))
				i = j + 2
				continue
			}
		case mdAt(r, i, "__"):
			if j := mdIndex(r, i+2, "__"); j >= 0 {
				b.WriteString(tbold(renderInline(string(r[i+2 : j]))))
				i = j + 2
				continue
			}
		case c == '*' || c == '_':
			if j := mdIndex(r, i+1, string(c)); j > i+1 {
				b.WriteString(titalic(string(r[i+1 : j])))
				i = j + 1
				continue
			}
		case c == '[':
			if cl := mdIndex(r, i+1, "]"); cl >= 0 && cl+1 < len(r) && r[cl+1] == '(' {
				if en := mdIndex(r, cl+2, ")"); en >= 0 {
					b.WriteString(tunder(renderInline(string(r[i+1 : cl]))))
					b.WriteString(tdim(" (" + string(r[cl+2:en]) + ")"))
					i = en + 1
					continue
				}
			}
		}
		b.WriteRune(c)
		i++
	}
	return b.String()
}

// mdAt reports whether sub starts at r[i].
func mdAt(r []rune, i int, sub string) bool {
	sr := []rune(sub)
	if i+len(sr) > len(r) {
		return false
	}
	return string(r[i:i+len(sr)]) == sub
}

// mdIndex finds sub in r at or after from, returning its rune index or -1.
func mdIndex(r []rune, from int, sub string) int {
	sr := []rune(sub)
	for i := from; i+len(sr) <= len(r); i++ {
		if string(r[i:i+len(sr)]) == sub {
			return i
		}
	}
	return -1
}

// collapseBlanks squeezes runs of blank lines to one and trims leading/trailing blanks, so
// rendered replies have even spacing without big gaps.
func collapseBlanks(lines []string) []string {
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		if l == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
		} else {
			prevBlank = false
		}
		out = append(out, l)
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
