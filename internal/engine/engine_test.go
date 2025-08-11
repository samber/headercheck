package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type fakeGit struct {
	author  string
	created string
	updated string
	touched bool
}

func (g *fakeGit) Author(_ string) (string, error)         { return g.author, nil }
func (g *fakeGit) CreationDate(_ string) (string, error)   { return g.created, nil }
func (g *fakeGit) LastUpdateDate(_ string) (string, error) { return g.updated, nil }
func (g *fakeGit) Touched(_ context.Context, _ string) (bool, error) {
	return g.touched, nil
}

func TestNormalizeNewlines(t *testing.T) {
	in := []byte("a\r\nb\r\nc\n")
	out := normalizeNewlines(in)
	if bytes.Contains(out, []byte("\r")) {
		t.Fatalf("expected CR characters to be removed: %q", out)
	}
}

func TestDetectHeaderBlock_WithShebang(t *testing.T) {
	content := []byte("#!/usr/bin/env bash\n# header line\n\necho hi\n")
	hdr, start, end := detectHeaderBlock(content)
	if !bytes.HasPrefix(content, []byte("#!/usr/bin/env bash\n")) {
		t.Fatalf("invalid test content")
	}
	if got, want := string(hdr), "# header line\n\n"; got != want {
		t.Fatalf("header mismatch: got %q want %q", got, want)
	}
	// start should skip shebang
	if start == 0 || end <= start {
		t.Fatalf("unexpected start/end: %d %d", start, end)
	}
}

func TestInsertHeader_PreservesShebang(t *testing.T) {
	content := []byte("#!/usr/bin/env bash\necho hi\n")
	header := []byte("# header\n")
	out := insertHeader(content, header)
	want := []byte("#!/usr/bin/env bash\n# header\n\necho hi\n")
	if !bytes.Equal(out, want) {
		t.Fatalf("insert result mismatch:\nGOT:\n%q\nWANT:\n%q", out, want)
	}
}

func TestReplaceHeader_EnsuresOneBlankLine(t *testing.T) {
	content := []byte("// old\n\npackage x\n")
	hdr, s, e := detectHeaderBlock(content)
	if len(hdr) == 0 {
		t.Fatalf("expected header detected")
	}
	out := replaceHeader(content, s, e, []byte("// new\n"))
	want := []byte("// new\n\npackage x\n")
	if !bytes.Equal(out, want) {
		t.Fatalf("replace result mismatch:\nGOT:\n%q\nWANT:\n%q", out, want)
	}
}

func TestGoDirectives_AreMovedBelowHeader_OnInsert(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// Header\n"))
	src := filepath.Join(dir, "hello.go")
	// File with go:build and nolint directives at top
	mustWrite(t, src, []byte("//go:build go1.22\n//nolint:errcheck\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	res, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res[0].Action != ActionInsert {
		t.Fatalf("expected insert, got: %+v", res[0])
	}
	b := mustRead(t, src)
	got := string(b)
	// exactly one blank line after header and after directives
	if !strings.HasPrefix(got, "// Header\n\n//go:build go1.22\n//nolint:errcheck\n\n") {
		t.Fatalf("directives should be below header, got:\n%s", got)
	}
}

func TestGoDirectives_AreMovedBelowHeader_OnReplace(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// New header\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("//go:build go1.20\n//nolint\n\n// Old header\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	res, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res[0].Action != ActionReplace {
		t.Fatalf("expected replace, got: %+v", res[0])
	}
	b := mustRead(t, src)
	got := string(b)
	if !strings.HasPrefix(got, "// New header\n\n//go:build go1.20\n//nolint\n\n") {
		t.Fatalf("directives should be below header after replace, got:\n%s", got)
	}
}

func TestShebang_RemainsVeryTop_WithHeaderAndDirectives(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("# header\n"))
	src := filepath.Join(dir, "script.sh")
	mustWrite(t, src, []byte("#!/usr/bin/env bash\n# shellcheck disable=SC2086\n\necho hi\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(`(?i)\.(sh)$`)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	res, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	// may be replace if test detected existing (non-empty) header-style lines
	if res[0].Action != ActionInsert && res[0].Action != ActionReplace {
		t.Fatalf("expected insert or replace, got: %+v", res[0])
	}
	got := string(mustRead(t, src))
	if !strings.HasPrefix(got, "#!/usr/bin/env bash\n\n# header\n\n# shellcheck disable=SC2086\n\n") {
		t.Fatalf("shebang/header/directive order incorrect:\n%s", got)
	}
}

func TestTopOfFileComments_MovedBelowHeader(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// H\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("// random top comment\n// another\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	_, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	got := string(mustRead(t, src))
	if !strings.HasPrefix(got, "// H\n\n// random top comment\n// another\n\n") {
		t.Fatalf("top-of-file comments should move below header:\n%s", got)
	}
}

func TestReplaceHeader_KeepsSingleBlankLine_AfterHeader(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// new\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("// old\n\n//go:build go1.20\n\npackage x\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	_, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	got := string(mustRead(t, src))
	// Expect exactly one blank line after header, then directives, then a blank line
	if !strings.HasPrefix(got, "// new\n\n//go:build go1.20\n\n") {
		t.Fatalf("unexpected spacing after replace:\n%s", got)
	}
}

func TestLegacyPlusBuild_Tag_Reordered(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// H\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("// +build linux\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	_, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	got := string(mustRead(t, src))
	if !strings.HasPrefix(got, "// H\n\n// +build linux\n\n") {
		t.Fatalf("legacy +build should be moved below header:\n%s", got)
	}
}

func TestGoGenerate_Pragma_Reordered(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// H\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("//go:generate echo hi\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	_, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	got := string(mustRead(t, src))
	if !strings.HasPrefix(got, "// H\n\n//go:generate echo hi\n\n") {
		t.Fatalf("go:generate should be moved below header:\n%s", got)
	}
}

func TestNoop_WhenHeaderAlreadyCorrect(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// H\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("// H\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	res, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res[0].Action != ActionNone {
		t.Fatalf("expected no change, got: %+v", res[0])
	}
}

func TestProcess_InsertWhenMissing(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// Header\n// Author: %author%\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("package main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, err := New(Options{Root: dir, Rules: rules, Git: &fakeGit{author: "Alice", touched: true}, RespectGit: true})
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}
	res, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if len(res) != 1 || res[0].Action != ActionInsert {
		t.Fatalf("unexpected result: %+v", res)
	}
	b := mustRead(t, src)
	if !bytes.HasPrefix(b, []byte("// Header\n")) {
		t.Fatalf("header not inserted: %q", b)
	}
}

func TestProcess_ReplaceWhenDifferent(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// wanted\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("// old\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	res, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res[0].Action != ActionReplace {
		t.Fatalf("expected replace, got: %+v", res[0])
	}
	if !bytes.HasPrefix(mustRead(t, src), []byte("// wanted\n")) {
		t.Fatalf("header not replaced")
	}
}

func TestProcess_SemanticMatch_NoFixWhenNotTouched(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	// date should be semantically ignored, license line fixed text
	mustWrite(t, tmpl, []byte("// Last update: %last_update_date%\n// License: Apache-2.0\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("// Last update: 2020-01-01\n// License: Apache-2.0\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{updated: "2024-02-02", touched: false}, RespectGit: true})
	res, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res[0].Action != ActionNone {
		t.Fatalf("expected no change due to git untouched, got: %+v", res[0])
	}
}

func TestProcess_SemanticMatch_FixWhenTouched(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// Last update: %last_update_date%\n// License: Apache-2.0\n"))
	src := filepath.Join(dir, "hello.go")
	mustWrite(t, src, []byte("// Last update: 2020-01-01\n// License: Apache-2.0\n\npackage main\n"))

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{updated: "2024-02-02", touched: true}, RespectGit: true})
	res, err := e.Process(context.Background(), []string{src}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res[0].Action != ActionReplace {
		t.Fatalf("expected replace to render new date, got: %+v", res[0])
	}
	if !bytes.Contains(mustRead(t, src), []byte("2024-02-02")) {
		t.Fatalf("expected updated date in header")
	}
}

func TestProcess_NonUTF8(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// header\n"))
	bin := filepath.Join(dir, "bin.go")
	mustWrite(t, bin, []byte{0xff, 0xfe, 0xfd, '\n', 'p', 'k', 'g'})

	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true, Force: true})
	res, err := e.Process(context.Background(), []string{bin}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res[0].Action != ActionNone || res[0].Warning == "" {
		t.Fatalf("expected warning and no action on non-UTF8 forced? got: %+v", res[0])
	}

	e2, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true, Force: false})
	res2, err := e2.Process(context.Background(), []string{bin}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res2[0].Action != ActionNone {
		t.Fatalf("expected no action on non-UTF8, got: %+v", res2[0])
	}
}

func TestProcess_SkipTemplateFile(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "tmpl.txt")
	mustWrite(t, tmpl, []byte("// header\n"))
	rules := []TemplateRule{{TemplatePath: tmpl, Include: regexp.MustCompile(DefaultIncludeRegex)}}
	e, _ := New(Options{Root: dir, Rules: rules, Git: &fakeGit{touched: true}, RespectGit: true})
	res, err := e.Process(context.Background(), []string{tmpl}, true)
	if err != nil {
		t.Fatalf("process error: %v", err)
	}
	if res[0].Action != ActionNone {
		t.Fatalf("expected skip template file, got: %+v", res[0])
	}
}

// --- helpers ---
func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o666); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
