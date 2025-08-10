package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

type GitMetadata interface {
	Author(path string) (string, error)
	CreationDate(path string) (string, error)
	LastUpdateDate(path string) (string, error)
	// Touched reports if file has changes compared to HEAD (or if newly added/unstaged).
	Touched(ctx context.Context, path string) (bool, error)
}

// DefaultIncludeRegex aims to match common source code file extensions across languages.
const DefaultIncludeRegex = `(?i)\.(go|c|h|hpp|hh|cc|cpp|cxx|cs|java|kt|ts|tsx|js|jsx|mjs|cjs|rb|py|rs|php|swift|m|mm|scala|sh|bash|zsh|fish|pl|pm|r|jl|sql|proto|make|mk|cmake|dockerfile|gradle|sbt|groovy|hs|erl|ex|exs|clj|cljs|edn|fs|fsi|fsx|ps1|psm1|vb|vbs|lua|coffee|dart|nim|zig)$`

type TemplateRule struct {
	TemplatePath string
	Include      *regexp.Regexp
	Exclude      *regexp.Regexp
	Content      []byte
}

type Options struct {
	Root       string
	Rules      []TemplateRule
	Force      bool
	Verbose    bool
	Git        GitMetadata
	RespectGit bool
}

type Engine struct {
	opts Options
}

func New(opts Options) (*Engine, error) {
	e := &Engine{opts: Options{Root: opts.Root, Force: opts.Force, Verbose: opts.Verbose, Git: opts.Git, RespectGit: opts.RespectGit}}
	for _, tr := range opts.Rules {
		if strings.TrimSpace(tr.TemplatePath) == "" {
			continue
		}
		b, err := os.ReadFile(tr.TemplatePath)
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", tr.TemplatePath, err)
		}
		e.opts.Rules = append(e.opts.Rules, TemplateRule{
			TemplatePath: tr.TemplatePath,
			Include:      tr.Include,
			Exclude:      tr.Exclude,
			Content:      normalizeNewlines(b),
		})
	}
	return e, nil
}

// FileResult describes the action taken or required for a file.
type FileResult struct {
	Path    string
	Action  Action
	Err     error
	Warning string
}

type Action string

const (
	ActionNone    Action = "none"
	ActionInsert  Action = "insert"
	ActionReplace Action = "replace"
	ActionRemove  Action = "remove"
)

func (e *Engine) Process(ctx context.Context, paths []string, fix bool) ([]FileResult, error) {
	var results []FileResult
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			results = append(results, FileResult{Path: p, Err: err})
			continue
		}
		if info.IsDir() {
			err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					results = append(results, FileResult{Path: path, Err: err})
					return nil
				}
				if d.IsDir() {
					// skip vendor and .git
					name := d.Name()
					if name == ".git" || name == "vendor" || name == ".idea" || name == ".vscode" || name == "node_modules" {
						return filepath.SkipDir
					}
					return nil
				}
				// // Skip template files themselves
				// for _, tr := range e.opts.Rules {
				// 	if filepath.Clean(path) == filepath.Clean(tr.TemplatePath) {
				// 		return nil
				// 	}
				// }
				fr := e.processFile(ctx, path, fix)
				results = append(results, fr)
				return nil
			})
			if err != nil {
				return results, err
			}
			continue
		}
		fr := e.processFile(ctx, p, fix)
		results = append(results, fr)
	}
	return results, nil
}

func (e *Engine) processFile(ctx context.Context, path string, fix bool) FileResult {
	if e.isTemplatePath(path) {
		return FileResult{Path: path, Action: ActionNone}
	}

	rel := e.relativePath(path)

	// If no template applies to this path (by include/exclude), skip early
	if !e.hasAnyTemplateForPath(rel) {
		return FileResult{Path: path, Action: ActionNone}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return FileResult{Path: path, Err: err}
	}

	if fr, ok := e.handleNonUTF8File(path, content); !ok {
		return fr
	}

	// Render templates for this file and detect current header
	trulesAll := e.renderTemplates(path)
	trules := e.filterTemplatesForPath(rel, trulesAll)
	if len(trules) == 0 {
		// No applicable template for this file; skip
		return FileResult{Path: path, Action: ActionNone}
	}
	currentHeader, headerStart, headerEnd := detectHeaderBlock(content)

	// Try to find a matching template for the current header
	matchedIdx := e.findMatchingTemplateIndex(rel, trules, currentHeader)

	if matchedIdx >= 0 {
		return e.handleMatchedHeader(ctx, path, fix, currentHeader, trules[matchedIdx], content, headerStart, headerEnd)
	}

	// No match with any template
	return e.handleNoMatch(ctx, path, fix, currentHeader, content, headerStart, headerEnd, trules)
}

// normalizeNewlines converts CRLF to LF
func normalizeNewlines(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// renderTemplates expands variables for a given file path.
func (e *Engine) renderTemplates(path string) []TemplateRule {
	// gather variables
	author, _ := e.opts.Git.Author(path)
	cr, _ := e.opts.Git.CreationDate(path)
	lu, _ := e.opts.Git.LastUpdateDate(path)
	if author == "" {
		author = "unknown"
	}
	if cr == "" {
		cr = ""
	}
	if lu == "" {
		lu = ""
	}
	var out []TemplateRule
	for _, tr := range e.opts.Rules {
		s := string(tr.Content)
		s = strings.ReplaceAll(s, "%author%", author)
		s = strings.ReplaceAll(s, "%creation_date%", cr)
		s = strings.ReplaceAll(s, "%last_update_date%", lu)
		out = append(out, TemplateRule{
			TemplatePath: tr.TemplatePath,
			Include:      tr.Include,
			Exclude:      tr.Exclude,
			Content:      []byte(s),
		})
	}
	return out
}

// headerSemanticallyMatches compares two headers ignoring dynamic variable values by masking variables.
func headerSemanticallyMatches(existing, expected []byte) bool {
	if len(existing) == 0 || len(expected) == 0 {
		return false
	}
	// Mask variable sections by replacing sequences matching typical date/hash/email with placeholders
	sanitize := func(s []byte) []byte {
		text := string(s)
		// dates like 2024-07-31, 2024/07/31, 31-07-2024 etc.
		text = regexp.MustCompile(`\b\d{4}[-/]?\d{2}[-/]?\d{2}\b`).ReplaceAllString(text, "<DATE>")
		// time
		text = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}\b`).ReplaceAllString(text, "<TIME>")
		// email
		text = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`).ReplaceAllString(text, "<EMAIL>")
		// hex hashes
		text = regexp.MustCompile(`\b[0-9a-fA-F]{7,40}\b`).ReplaceAllString(text, "<HASH>")
		// years
		text = regexp.MustCompile(`\b(19|20)\d{2}\b`).ReplaceAllString(text, "<YEAR>")
		// collapse multiple spaces
		text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
		return []byte(strings.TrimSpace(text))
	}
	a := sanitize(existing)
	b := sanitize(expected)
	return bytes.Equal(a, b)
}

// detectHeaderBlock extracts the leading header comment block including shebang handling.
func detectHeaderBlock(content []byte) (header []byte, start int, end int) {
	r := bufio.NewReader(bytes.NewReader(content))
	var buf bytes.Buffer
	var off int
	// Preserve shebang if present
	firstLine, _ := r.ReadString('\n')
	if strings.HasPrefix(firstLine, "#!") {
		off += len(firstLine)
		// read next block of comments after shebang
		// include shebang in header detection only for reinsertion location, not comparison
		// so we continue collecting comments
	} else {
		// reset reader to start if not shebang
		r.Reset(bytes.NewReader(content))
		off = 0
	}

	// Collect leading comments (#, //, ; comment for some files) and block comments
	// Stop at first non-comment non-empty line
	for {
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			break
		}
		// End conditions
		if !(isLineComment(line) || strings.TrimSpace(line) == "") {
			// reached first code line
			break
		}
		buf.WriteString(line)
		if errors.Is(err, io.EOF) {
			break
		}
	}

	headerBytes := buf.Bytes()
	if len(headerBytes) == 0 {
		return nil, 0, 0
	}
	return headerBytes, off, off + len(headerBytes)
}

func isLineComment(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "//") || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
		return true
	}
	// block comment start or end lines are counted as comments too
	if strings.HasPrefix(s, "/*") || strings.HasPrefix(s, "*") || strings.HasPrefix(s, "*/") {
		return true
	}
	return false
}

func replaceHeader(content []byte, start, end int, header []byte) []byte {
	var out bytes.Buffer
	out.Write(content[:start])
	out.Write(header)
	// ensure exactly one blank line after header
	tail := bytes.TrimLeft(content[end:], "\n\r")
	out.WriteString("\n")
	out.Write(tail)
	return out.Bytes()
}

func insertHeader(content []byte, header []byte) []byte {
	// Preserve shebang
	if bytes.HasPrefix(content, []byte("#!")) {
		nl := bytes.IndexByte(content, '\n')
		if nl >= 0 {
			var out bytes.Buffer
			out.Write(content[:nl+1])
			out.Write(header)
			out.WriteString("\n")
			out.Write(bytes.TrimLeft(content[nl+1:], "\n\r"))
			return out.Bytes()
		}
	}
	var out bytes.Buffer
	out.Write(header)
	out.WriteString("\n")
	out.Write(bytes.TrimLeft(content, "\n\r"))
	return out.Bytes()
}

// ---- Small helper functions to keep processFile readable ----

func (e *Engine) isTemplatePath(path string) bool {
	for _, tr := range e.opts.Rules {
		if filepath.Clean(path) == filepath.Clean(tr.TemplatePath) {
			return true
		}
	}
	return false
}

func (e *Engine) relativePath(path string) string {
	rel, err := filepath.Rel(e.opts.Root, path)
	if err != nil {
		return path
	}
	return rel
}

// hasAnyTemplateForPath checks if at least one template rule applies to a given relative path
// according to include/exclude patterns.
func (e *Engine) hasAnyTemplateForPath(rel string) bool {
	for _, tr := range e.opts.Rules {
		if tr.Exclude != nil && tr.Exclude.MatchString(rel) {
			continue
		}
		if tr.Include != nil && !tr.Include.MatchString(rel) {
			continue
		}
		return true
	}
	return false
}

// handleNonUTF8File returns an early FileResult if the file is non-UTF8 and Force is not set.
// If the file is acceptable (UTF-8 or forced), returns an empty result and ok=true.
func (e *Engine) handleNonUTF8File(path string, content []byte) (FileResult, bool) {
	if utf8.Valid(content) {
		return FileResult{}, true
	}
	if e.opts.Force {
		return FileResult{Path: path, Warning: "non-UTF8/binary file, forced to check", Action: ActionNone}, false
	}
	return FileResult{Path: path, Action: ActionNone}, false
}

func (e *Engine) findMatchingTemplateIndex(rel string, trules []TemplateRule, currentHeader []byte) int {
	for i, tr := range trules {
		if tr.Exclude != nil && tr.Exclude.MatchString(rel) {
			continue
		}
		if tr.Include != nil && !tr.Include.MatchString(rel) {
			continue
		}
		if headerSemanticallyMatches(currentHeader, tr.Content) {
			if e.opts.Verbose {
				fmt.Printf("header matched template for %s\n", rel)
			}
			return i
		}
	}
	return -1
}

// filterTemplatesForPath returns only the templates whose include/exclude accept the given relative path.
func (e *Engine) filterTemplatesForPath(rel string, trules []TemplateRule) []TemplateRule {
	filtered := make([]TemplateRule, 0, len(trules))
	for _, tr := range trules {
		if tr.Exclude != nil && tr.Exclude.MatchString(rel) {
			continue
		}
		if tr.Include != nil && !tr.Include.MatchString(rel) {
			continue
		}
		filtered = append(filtered, tr)
	}
	return filtered
}

func (e *Engine) shouldSkipDueToGit(ctx context.Context, path string) bool {
	if !e.opts.RespectGit || e.opts.Git == nil {
		return false
	}
	touched, _ := e.opts.Git.Touched(ctx, path)
	return !touched
}

func (e *Engine) handleMatchedHeader(
	ctx context.Context,
	path string,
	fix bool,
	currentHeader []byte,
	rendered TemplateRule,
	content []byte,
	headerStart, headerEnd int,
) FileResult {
	if !fix {
		return FileResult{Path: path, Action: ActionNone}
	}
	if bytes.Equal(currentHeader, rendered.Content) {
		return FileResult{Path: path, Action: ActionNone}
	}
	// Only suppress updates on clean trees when the difference is purely variable-like
	// (i.e., headers are a semantic match ignoring variable values). In that case,
	// we consider the existing header acceptable and avoid churn.
	if e.shouldSkipDueToGit(ctx, path) && headerSemanticallyMatches(currentHeader, rendered.Content) {
		return FileResult{Path: path, Action: ActionNone}
	}
	nb := replaceHeader(content, headerStart, headerEnd, rendered.Content)
	if err := os.WriteFile(path, nb, 0o666); err != nil {
		return FileResult{Path: path, Err: err}
	}
	return FileResult{Path: path, Action: ActionReplace}
}

func (e *Engine) handleNoMatch(
	ctx context.Context,
	path string,
	fix bool,
	currentHeader []byte,
	content []byte,
	headerStart, headerEnd int,
	trules []TemplateRule,
) FileResult {
	if len(trules) == 0 {
		return FileResult{Path: path, Action: ActionNone}
	}
	if fix {
		if len(currentHeader) > 0 {
			nb := replaceHeader(content, headerStart, headerEnd, trules[0].Content)
			if err := os.WriteFile(path, nb, 0o666); err != nil {
				return FileResult{Path: path, Err: err}
			}
			return FileResult{Path: path, Action: ActionReplace}
		}
		nb := insertHeader(content, trules[0].Content)
		if err := os.WriteFile(path, nb, 0o666); err != nil {
			return FileResult{Path: path, Err: err}
		}
		return FileResult{Path: path, Action: ActionInsert}
	}
	if len(currentHeader) > 0 {
		return FileResult{Path: path, Action: ActionReplace}
	}
	return FileResult{Path: path, Action: ActionInsert}
}
