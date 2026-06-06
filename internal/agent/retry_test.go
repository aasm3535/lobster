package agent

import (
	"errors"
	"testing"
)

func TestTransientErr(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"anthropic http 429: rate limited", true},
		{"anthropic http 408: timeout", true},
		{"anthropic http 500: oops", true},
		{"anthropic http 503: overloaded", true},
		{"Post \"https://x\": EOF", true},
		{"stream read: connection reset by peer", true},
		{"anthropic http 401: bad key", false},
		{"anthropic http 400: invalid request", false},
		{"openai http 404: no such model", false},
	}
	for _, c := range cases {
		if got := transientErr(errors.New(c.err)); got != c.want {
			t.Errorf("transientErr(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}
