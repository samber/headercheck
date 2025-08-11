// Package engine provides the main engine for headercheck.
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

// GitMetadata describes the Git metadata for a file.
type GitMetadata interface {
	Author(path string) (string, error)
	CreationDate(path string) (string, error)
	LastUpdateDate(path string) (string, error)
	// Touched reports if file has changes compared to HEAD (or if newly added/unstaged).
	Touched(ctx context.Context, path string) (bool, error)
}

// DefaultIncludeRegex aims to match common source code file extensions across languages.
const DefaultIncludeRegex = `(?i)\.(go|c|h|hpp|hh|cc|cpp|cxx|cs|java|kt|ts|tsx|js|jsx|mjs|cjs|rb|py|rs|php|swift|m|mm|scala|sh|bash|zsh|fish|pl|pm|r|jl|sql|proto|make|mk|cmake|dockerfile|gradle|sbt|groovy|hs|erl|ex|exs|clj|cljs|edn|fs|fsi|fsx|ps1|psm1|vb|vbs|lua|coffee|dart|nim|zig)$`

// TemplateRule describes a template rule.
type TemplateRule struct {
	TemplatePath string
	Include      *regexp.Regexp
	Exclude      *regexp.Regexp
	Content      []byte
}

// Options describes the options for the engine.
type Options struct {
	Root       string
	Rules      []TemplateRule
	Force      bool
	Verbose    bool
	Git        GitMetadata
	RespectGit bool
}

// Engine is the main engine for headercheck.
type Engine struct {
	opts Options
}

// New creates a new engine.
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

// Action describes the action taken or required for a file.
type Action string

const (
	// ActionNone indicates that no action is required for the file.
	ActionNone Action = "none"
	// ActionInsert indicates that the header should be inserted for the file.
	ActionInsert Action = "insert"
	// ActionReplace indicates that the header should be replaced for the file.
	ActionReplace Action = "replace"
	// ActionRemove indicates that the header should be removed for the file.
	ActionRemove Action = "remove"
)

// Process checks and fixes the headers for the given paths.
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

// processFile checks and fixes the header for the given path.
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
	currentHeader, _, _ := detectHeaderBlock(content)

	// Try to find a matching template for the current header
	matchedIdx := e.findMatchingTemplateIndex(rel, trules, currentHeader)

	if matchedIdx >= 0 {
		return e.handleMatchedHeader(ctx, path, fix, currentHeader, trules[matchedIdx], content)
	}

	// No match with any template
	return e.handleNoMatch(ctx, path, fix, currentHeader, content, trules)
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

// headersStructurallyEqual considers headers equal if they only differ by trailing
// newlines/blank lines. This avoids churn when the template omits the blank line
// that is present in detected header blocks.
func headersStructurallyEqual(a, b []byte) bool {
	aa := bytes.TrimRight(a, "\r\n")
	bb := bytes.TrimRight(b, "\r\n")
	return bytes.Equal(aa, bb)
}

// detectHeaderBlock extracts the leading header comment block including shebang handling.
func detectHeaderBlock(content []byte) (header []byte, start int, end int) {
	// Start after shebang and skip directives to detect existing header block
	pos := findShebangEnd(content)
	pos = skipBlankAndDirectives(content, pos)

	// Collect contiguous header comment block
	r := bufio.NewReader(bytes.NewReader(content[pos:]))
	var buf bytes.Buffer
	for {
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !isLineComment(line) {
			break
		}
		// Exclude directive lines from header
		if isGoPreambleDirective(line) {
			break
		}
		buf.WriteString(line)
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if buf.Len() == 0 {
		return nil, 0, 0
	}
	headerBytes := buf.Bytes()
	return headerBytes, pos, pos + len(headerBytes)
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

// isGoPreambleDirective reports whether the line is a Go-specific
// top-of-file directive that must stay above any header, such as:
//   - //go:build ...   (or legacy // +build ...)
//   - //go:generate ... (and other //go:* pragmas)
//   - //nolint[:...] ... (file-wide linter directives)
//   - //lint:...        (additional linter directives)
func isGoPreambleDirective(line string) bool {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "//") {
		// Recognize non-Go directive styles for common tools (eg shellcheck)
		if strings.HasPrefix(s, "#") {
			s2 := strings.TrimSpace(strings.TrimPrefix(s, "#"))
			if strings.HasPrefix(strings.ToLower(s2), "shellcheck") {
				return true
			}
		}
		return false
	}
	// strip leading slashes and spaces
	s = strings.TrimSpace(strings.TrimPrefix(s, "//"))
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "go:") { // covers go:build, go:generate, go:linkname, ...
		return true
	}
	if strings.HasPrefix(s, "+build") { // legacy build tags
		return true
	}
	if strings.HasPrefix(s, "nolint") { // file-wide linter ignores
		return true
	}
	if strings.HasPrefix(s, "lint:") {
		return true
	}
	return false
}

// detectPreambleEnd returns the byte offset right after any shebang and
// Go preamble directives that must be kept at the very top of the file.
// It also includes a trailing blank line after the last directive when present
// (required by Go build constraints), so headers are inserted below that line.
// findShebangEnd returns the end offset of a shebang line if present, else 0.
func findShebangEnd(content []byte) int {
	if bytes.HasPrefix(content, []byte("#!")) {
		if nl := bytes.IndexByte(content, '\n'); nl >= 0 {
			return nl + 1
		}
		return len(content)
	}
	return 0
}

// skipBlankAndDirectives advances from pos past blank lines and directive lines.
func skipBlankAndDirectives(content []byte, pos int) int {
	for pos < len(content) {
		// find end of current line
		nl := bytes.IndexByte(content[pos:], '\n')
		end := len(content)
		if nl >= 0 {
			end = pos + nl + 1
		}
		line := string(content[pos:end])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isGoPreambleDirective(line) {
			pos = end
			continue
		}
		break
	}
	return pos
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
	// Insert header after shebang (if any), before any directives
	pos := findShebangEnd(content)

	var out bytes.Buffer
	out.Write(content[:pos])
	out.Write(header)
	out.WriteString("\n")
	out.Write(bytes.TrimLeft(content[pos:], "\n\r"))
	return out.Bytes()
}

// upsertHeaderBeforeDirectives ensures the given header exists below the shebang
// and before any Go preamble directives. If a header already exists, it will be
// replaced and any directive lines will be kept (and relocated below the header
// if they were above it).
func upsertHeaderBeforeDirectives(content []byte, header []byte, preserveExisting bool) []byte {
	// Extract existing header block
	existing, start, end := detectHeaderBlock(content)
	shebangEnd := findShebangEnd(content)

	// Split content into three parts: shebang, middle, tail
	head := content[:shebangEnd]
	middle := content[shebangEnd:]

	// From middle, capture any leading directives and blanks
	// so we can place header before them
	dirStart := 0
	for dirStart < len(middle) {
		nl := bytes.IndexByte(middle[dirStart:], '\n')
		lineEnd := len(middle)
		if nl >= 0 {
			lineEnd = dirStart + nl + 1
		}
		line := string(middle[dirStart:lineEnd])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isGoPreambleDirective(line) {
			dirStart = lineEnd
			if nl < 0 {
				break
			}
			continue
		}
		break
	}
	directivesAndBlanks := middle[:dirStart]

	// Next, capture top-of-file non-directive comment block (and its trailing blanks)
	// that immediately follows the directives OR begins the file when no directives exist.
	// This block should be moved below the directives, keeping it below the header.
	commEnd := dirStart
	for commEnd < len(middle) {
		nl := bytes.IndexByte(middle[commEnd:], '\n')
		lineEnd := len(middle)
		if nl >= 0 {
			lineEnd = commEnd + nl + 1
		}
		line := string(middle[commEnd:lineEnd])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			commEnd = lineEnd
			if nl < 0 {
				break
			}
			continue
		}
		// treat any comment line as top-of-file comments, but stop before package decl or code
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			commEnd = lineEnd
			if nl < 0 {
				break
			}
			continue
		}
		break
	}
	topComments := middle[dirStart:commEnd]
	rest := middle[commEnd:]

	// If an existing header was detected and it lies within head+directives region,
	// remove it prior to re-inserting. Optionally preserve the removed block to
	// relocate it below the header.
	var preservedHeader []byte
	if len(existing) > 0 && start >= shebangEnd {
		// delete existing header range
		if preserveExisting {
			preservedHeader = make([]byte, len(existing))
			copy(preservedHeader, existing)
		}
		var buf bytes.Buffer
		buf.Write(content[:start])
		buf.Write(content[end:])
		content = buf.Bytes()
		// recompute pieces
		shebangEnd = findShebangEnd(content)
		head = content[:shebangEnd]
		middle = content[shebangEnd:]
		// recompute directives
		dirStart = 0
		for dirStart < len(middle) {
			nl := bytes.IndexByte(middle[dirStart:], '\n')
			lineEnd := len(middle)
			if nl >= 0 {
				lineEnd = dirStart + nl + 1
			}
			line := string(middle[dirStart:lineEnd])
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || isGoPreambleDirective(line) {
				dirStart = lineEnd
				if nl < 0 {
					break
				}
				continue
			}
			break
		}
		directivesAndBlanks = middle[:dirStart]
		// recompute top-of-file comments block
		commEnd = dirStart
		for commEnd < len(middle) {
			nl := bytes.IndexByte(middle[commEnd:], '\n')
			lineEnd := len(middle)
			if nl >= 0 {
				lineEnd = commEnd + nl + 1
			}
			line := string(middle[commEnd:lineEnd])
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || isLineComment(line) {
				commEnd = lineEnd
				if nl < 0 {
					break
				}
				continue
			}
			break
		}
		topComments = middle[dirStart:commEnd]
		rest = middle[commEnd:]
	}

	var out bytes.Buffer
	out.Write(head)
	// Ensure exactly one blank line between shebang and header (if shebang present)
	if shebangEnd > 0 {
		out.WriteString("\n")
	}
	out.Write(header)
	// Ensure exactly one blank line after header
	if !bytes.HasSuffix(header, []byte("\n")) {
		out.WriteString("\n")
	}
	out.WriteString("\n")
	// Directives block normalized to have exactly one blank line after
	if trimmedDir := bytes.Trim(directivesAndBlanks, "\n\r"); len(trimmedDir) > 0 {
		out.Write(trimmedDir)
		out.WriteString("\n\n")
	}
	// Any other top-of-file comments moved after directives
	if trimmedTop := bytes.Trim(topComments, "\n\r"); len(trimmedTop) > 0 {
		out.Write(trimmedTop)
		out.WriteString("\n\n")
	}
	// If we preserved an existing header (non-template comments), append it as well
	if len(bytes.TrimSpace(preservedHeader)) > 0 {
		out.Write(bytes.Trim(preservedHeader, "\n\r"))
		out.WriteString("\n\n")
	}
	out.Write(rest)
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
) FileResult {
	if !fix {
		return FileResult{Path: path, Action: ActionNone}
	}
	if bytes.Equal(currentHeader, rendered.Content) {
		return FileResult{Path: path, Action: ActionNone}
	}
	if headersStructurallyEqual(currentHeader, rendered.Content) {
		return FileResult{Path: path, Action: ActionNone}
	}
	// If the only differences are variable-like and the file hasn't been touched,
	// skip updates to avoid churn. Otherwise, still update to refresh variables.
	if headerSemanticallyMatches(currentHeader, rendered.Content) && e.shouldSkipDueToGit(ctx, path) {
		return FileResult{Path: path, Action: ActionNone}
	}
	// Reorder so that header is after shebang and before any directives
	nb := upsertHeaderBeforeDirectives(content, rendered.Content, true)
	if err := os.WriteFile(path, nb, 0o666); err != nil {
		return FileResult{Path: path, Err: err}
	}
	return FileResult{Path: path, Action: ActionReplace}
}

func (e *Engine) handleNoMatch(
	_ context.Context,
	path string,
	fix bool,
	currentHeader []byte,
	content []byte,
	trules []TemplateRule,
) FileResult {
	if len(trules) == 0 {
		return FileResult{Path: path, Action: ActionNone}
	}
	if fix {
		nb := upsertHeaderBeforeDirectives(content, trules[0].Content, true)
		action := ActionInsert
		if len(currentHeader) > 0 {
			action = ActionReplace
		}
		if err := os.WriteFile(path, nb, 0o666); err != nil {
			return FileResult{Path: path, Err: err}
		}
		return FileResult{Path: path, Action: action}
	}
	if len(currentHeader) > 0 {
		return FileResult{Path: path, Action: ActionReplace}
	}
	return FileResult{Path: path, Action: ActionInsert}
}
