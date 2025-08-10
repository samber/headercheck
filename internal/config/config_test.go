package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DefaultsWhenNoConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load("", dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Templates) != 1 {
		t.Fatalf("expected 1 default template, got %d", len(cfg.Templates))
	}
	if cfg.Templates[0].Path != filepath.Join(dir, ".header.txt") {
		t.Fatalf("unexpected default path: %s", cfg.Templates[0].Path)
	}
}

func TestLoad_ParsesTemplatesStringAndObjects(t *testing.T) {
	dir := t.TempDir()
	conf := []byte(`
templates:
  - ".header.txt"
  - path: "foo.txt"
    include: "^src/"
    exclude: "^vendor/"
include: ".*\\.go$"
`)
	mustWrite(t, filepath.Join(dir, ".headercheck.yaml"), conf)
	cfg, err := Load("", dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Templates) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(cfg.Templates))
	}
	if cfg.Templates[0].Path != filepath.Join(dir, ".header.txt") {
		t.Fatalf("first template path normalized")
	}
	if cfg.Templates[1].Include == "" || cfg.Templates[1].Exclude == "" {
		t.Fatalf("object template include/exclude not parsed")
	}
}

func TestLoad_AppliesGlobalDefaults(t *testing.T) {
	dir := t.TempDir()
	conf := []byte(`
templates:
  - path: "one.txt"
  - path: "two.txt"
include: "^src/"
exclude: "^vendor/"
`)
	mustWrite(t, filepath.Join(dir, ".headercheck.yaml"), conf)
	cfg, err := Load("", dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, tdef := range cfg.Templates {
		if tdef.Include != "^src/" || tdef.Exclude != "^vendor/" {
			t.Fatalf("global defaults not applied: %+v", tdef)
		}
	}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o666); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
