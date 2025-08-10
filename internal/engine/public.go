package engine

import "bytes"

// Export thin wrappers for analyzer usage without duplicating logic.

// DetectHeaderBlock detects the header block in the given content.
func DetectHeaderBlock(content []byte) (header []byte, start int, end int) {
	return detectHeaderBlock(content)
}

// HeaderSemanticallyMatches reports if the existing header semantically matches the expected header.
func HeaderSemanticallyMatches(existing, expected []byte) bool {
	return headerSemanticallyMatches(existing, expected)
}

// InsertHeader inserts the given header into the given content.
func InsertHeader(content []byte, header []byte) []byte { return insertHeader(content, header) }

// ReplaceHeader replaces the header in the given content.
func ReplaceHeader(content []byte, start, end int, header []byte) []byte {
	return replaceHeader(content, start, end, header)
}

// NormalizeNewlines normalizes the newlines in the given content.
func NormalizeNewlines(b []byte) []byte { return normalizeNewlines(b) }

// RenderTemplatesFor renders the templates for the given path.
// RenderTemplatesFor path uses Engine's templates and Git metadata; if Engine is nil or has no templates,
// returns nil.
func (e *Engine) RenderTemplatesFor(path string) [][]byte {
	trs := e.renderTemplates(path)
	out := make([][]byte, 0, len(trs))
	for _, tr := range trs {
		out = append(out, tr.Content)
	}
	return out
}

// ConcatLines converts bytes to a single line for messages.
func ConcatLines(b []byte) string { return string(bytes.ReplaceAll(b, []byte("\n"), []byte(" "))) }

// AcceptsPath reports whether any template rule applies to the given path
// once include/exclude are evaluated. Path can be absolute or relative; it
// will be made relative to Engine root.
func (e *Engine) AcceptsPath(path string) bool {
	rel := e.relativePath(path)
	return e.hasAnyTemplateForPath(rel)
}

// RenderTemplatesForFiltered renders templates and returns only those whose
// include/exclude patterns accept the given path.
func (e *Engine) RenderTemplatesForFiltered(path string) [][]byte {
	rel := e.relativePath(path)
	trs := e.renderTemplates(path)
	filtered := e.filterTemplatesForPath(rel, trs)
	out := make([][]byte, 0, len(filtered))
	for _, tr := range filtered {
		out = append(out, tr.Content)
	}
	return out
}
