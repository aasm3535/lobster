package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandEnvRefs(t *testing.T) {
	t.Setenv("LOBSTER_T_KEY", "secret123")
	got := expandEnvRefs(`{"api_key":"${LOBSTER_T_KEY}","raw":"$LOBSTER_T_KEY","price":"a$b"}`)
	want := `{"api_key":"secret123","raw":"$LOBSTER_T_KEY","price":"a$b"}`
	if got != want {
		t.Fatalf("expandEnvRefs:\n got=%q\nwant=%q", got, want)
	}
}

func TestLoadDotEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	body := "# a comment\nLOBSTER_T_FOO=bar\nLOBSTER_T_QUOTED=\"hello world\"\nLOBSTER_T_EXISTING=fromfile\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LOBSTER_T_EXISTING", "fromenv") // a real env var must win over the file
	loadDotEnv(p)

	if got := os.Getenv("LOBSTER_T_FOO"); got != "bar" {
		t.Fatalf("FOO = %q", got)
	}
	if got := os.Getenv("LOBSTER_T_QUOTED"); got != "hello world" {
		t.Fatalf("QUOTED = %q (quotes should be stripped)", got)
	}
	if got := os.Getenv("LOBSTER_T_EXISTING"); got != "fromenv" {
		t.Fatalf("real env should win, got %q", got)
	}
}
