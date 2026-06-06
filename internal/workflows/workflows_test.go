package workflows

import (
	"path/filepath"
	"testing"
)

func TestSaveGetListDelete(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Save("Deploy Check", "# Deploy check\n1. build\n2. test\n3. ship"); err != nil {
		t.Fatal(err)
	}

	// Name is sanitized to a flat slug.
	body, ok := s.Get("deploy-check")
	if !ok {
		t.Fatalf("expected workflow deploy-check to exist")
	}
	if body == "" {
		t.Fatal("empty body")
	}

	list := s.List()
	if len(list) != 1 || list[0].Name != "deploy-check" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list[0].Description != "Deploy check" {
		t.Fatalf("description = %q, want heading text", list[0].Description)
	}

	if !s.Delete("deploy-check") {
		t.Fatal("delete reported false")
	}
	if _, ok := s.Get("deploy-check"); ok {
		t.Fatal("still exists after delete")
	}
}

func TestSanitizeBlocksTraversal(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A hostile name must not escape the directory.
	if err := s.Save("../../evil", "x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("....evil"); !ok {
		// sanitize("../../evil") -> "....evil" (slashes dropped, dots trimmed at the ends)
		t.Log("sanitized name differs — fine as long as it stayed inside the dir")
	}
	matches, _ := filepath.Glob(filepath.Join(s.Dir(), "*.md"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one file inside the store dir, got %v", matches)
	}
}

func TestGetMissing(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, ok := s.Get("nope"); ok {
		t.Fatal("Get of missing workflow returned ok")
	}
	if err := s.Save("", "body"); err == nil {
		t.Fatal("empty name should fail")
	}
}
