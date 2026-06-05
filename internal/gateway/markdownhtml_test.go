package gateway

import "testing"

func TestMdToHTML(t *testing.T) {
	cases := map[string]string{
		"**bold**":             "<b>bold</b>",
		"some *italic* word":   "some <i>italic</i> word",
		"use `ls -la` here":    "use <code>ls -la</code> here",
		"# Heading":            "<b>Heading</b>",
		"- one\n- two":         "• one\n• two",
		"* star bullet":        "• star bullet",
		"[Lobster](https://x)": `<a href="https://x">Lobster</a>`,
		"a < b && c > d":       "a &lt; b &amp;&amp; c &gt; d",
		"plain text":           "plain text",
		"__also bold__":        "<b>also bold</b>",
	}
	for in, want := range cases {
		if got := mdToHTML(in); got != want {
			t.Errorf("mdToHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMdToHTML_FencedCodeIsNotReformatted(t *testing.T) {
	in := "before\n```go\nx := a < b && **not bold**\n```\nafter"
	got := mdToHTML(in)
	want := "before\n<pre>x := a &lt; b &amp;&amp; **not bold**</pre>\nafter"
	if got != want {
		t.Fatalf("mdToHTML fenced:\n got=%q\nwant=%q", got, want)
	}
}

func TestMdToHTML_CodeProtectsSpecials(t *testing.T) {
	// snake_case inside code must not become italic, and < > & inside code are escaped.
	got := mdToHTML("call `do_a_thing(x < y & z)` now")
	want := "call <code>do_a_thing(x &lt; y &amp; z)</code> now"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
