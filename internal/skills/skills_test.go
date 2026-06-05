package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	meta, body := parseFrontmatter("---\nname: pdf\ndescription: \"Work with PDFs.\"\n---\n\n# Do the thing\nstep one\n")
	if meta["name"] != "pdf" {
		t.Fatalf("name = %q", meta["name"])
	}
	if meta["description"] != "Work with PDFs." {
		t.Fatalf("description = %q", meta["description"])
	}
	if body != "# Do the thing\nstep one" {
		t.Fatalf("body = %q", body)
	}

	// No frontmatter → whole content is body.
	_, b2 := parseFrontmatter("just instructions")
	if b2 != "just instructions" {
		t.Fatalf("body = %q", b2)
	}
}

func TestStore_DiscoversSkillsAndFiles(t *testing.T) {
	root := t.TempDir()

	// A valid skill with a bundled script.
	skillDir := filepath.Join(root, "greeter")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(skillDir, "SKILL.md"),
		"---\nname: greeter\ndescription: Say hi nicely.\n---\n\nRun scripts/hi.sh\n")
	mustWrite(t, filepath.Join(skillDir, "scripts", "hi.sh"), "echo hi\n")

	// A folder without SKILL.md must be ignored.
	if err := os.MkdirAll(filepath.Join(root, "not-a-skill"), 0o755); err != nil {
		t.Fatal(err)
	}

	s, err := Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(s.List()) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(s.List()))
	}
	sk, ok := s.Get("greeter")
	if !ok {
		t.Fatal("greeter not found")
	}
	if sk.Description != "Say hi nicely." {
		t.Fatalf("description = %q", sk.Description)
	}
	if len(sk.Files) != 1 || sk.Files[0] != "scripts/hi.sh" {
		t.Fatalf("files = %v", sk.Files)
	}

	// Reload picks up a newly added skill (as the agent would after authoring one).
	mustWrite(t, filepath.Join(root, "second", "SKILL.md"), "---\nname: second\ndescription: x\n---\nbody")
	if err := s.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(s.List()) != 2 {
		t.Fatalf("expected 2 skills after reload, got %d", len(s.List()))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
