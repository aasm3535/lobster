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
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
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

		// A pipe row followed by a |---|---| separator is a table — render it aligned.
		if isTableSep(strings.TrimSpace(at(lines, i+1))) && strings.Contains(tr, "|") && tr != "" {
			block, n := renderTable(lines, i)
			out = append(out, block...)
			i += n - 1
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

// at safely returns lines[i] or "" when out of range.
func at(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return lines[i]
}

// isTableSep reports whether a line is a markdown table separator (|---|:--:|---|).
func isTableSep(t string) bool {
	if !strings.Contains(t, "-") || !strings.Contains(t, "|") {
		return false
	}
	for _, r := range t {
		switch r {
		case '|', '-', ':', ' ', '\t':
		default:
			return false
		}
	}
	return true
}

// splitRow parses a "| a | b |" row into trimmed cells (outer pipes dropped).
func splitRow(t string) []string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// renderTable renders a markdown table starting at lines[start] as aligned columns (header
// bold, a dim rule under it, no vertical bars), and returns the rendered lines plus how many
// source lines it consumed.
func renderTable(lines []string, start int) ([]string, int) {
	var rows [][]string
	i := start
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || !strings.Contains(t, "|") {
			break
		}
		if isTableSep(t) {
			continue // the |---| line isn't data
		}
		rows = append(rows, splitRow(t))
	}
	consumed := i - start
	if len(rows) == 0 {
		return nil, consumed
	}

	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	// Column widths from the rendered (visible) cell text.
	width := make([]int, cols)
	rendered := make([][]string, len(rows))
	for ri, r := range rows {
		rendered[ri] = make([]string, cols)
		for ci := 0; ci < cols; ci++ {
			cell := ""
			if ci < len(r) {
				cell = renderInline(r[ci])
			}
			rendered[ri][ci] = cell
			if w := visibleWidth(cell); w > width[ci] {
				width[ci] = w
			}
		}
	}

	pad := func(cell string, w int) string {
		if d := w - visibleWidth(cell); d > 0 {
			return cell + strings.Repeat(" ", d)
		}
		return cell
	}

	var out []string
	total := 0
	for ri, r := range rendered {
		var cells []string
		for ci := 0; ci < cols; ci++ {
			c := pad(r[ci], width[ci])
			if ri == 0 {
				c = tbold(c)
			}
			cells = append(cells, c)
		}
		line := "  " + strings.Join(cells, "   ")
		out = append(out, line)
		if ri == 0 {
			for _, w := range width {
				total += w + 3
			}
			out = append(out, tdim("  "+strings.Repeat("─", total-3)))
		}
	}
	return out, consumed
}

// visibleWidth counts the display columns of s, skipping ANSI escape sequences.
func visibleWidth(s string) int {
	n, inEsc := 0, false
	for _, r := range s {
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		n++
	}
	return n
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
