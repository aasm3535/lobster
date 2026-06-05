package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Re-running setup must never wipe unrelated secrets already in .env (other providers'
// keys, MCP tokens). upsertEnv updates the keys it owns and leaves the rest untouched.
func TestUpsertEnvPreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	seed := "# secrets\nNVIDIA_API_KEY=nv-keep-me\nLOBSTER_API_KEY=old\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := upsertEnv(path, map[string]string{
		"LOBSTER_TELEGRAM_TOKEN": "tok",
		"LOBSTER_API_KEY":        "new",
	}); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(path)
	s := string(got)
	for _, want := range []string{
		"NVIDIA_API_KEY=nv-keep-me",  // untouched
		"LOBSTER_API_KEY=new",        // updated in place
		"LOBSTER_TELEGRAM_TOKEN=tok", // appended
		"# secrets",                  // comment kept
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "LOBSTER_API_KEY=old") {
		t.Errorf("stale value not replaced:\n%s", s)
	}
	if c := strings.Count(s, "LOBSTER_API_KEY="); c != 1 {
		t.Errorf("expected 1 LOBSTER_API_KEY line, got %d", c)
	}
}
