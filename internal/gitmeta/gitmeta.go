package gitmeta

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type Git struct {
	root     string
	disabled bool
}

func New(ctx context.Context, root string) (*Git, error) {
	// ensure git is available and repo exists
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git not available or not a repo: %w", err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return nil, fmt.Errorf("not a git work tree")
	}
	return &Git{root: root}, nil
}

func Disabled() *Git { return &Git{disabled: true} }

func (g *Git) Author(path string) (string, error) {
	if g.disabled {
		return "", nil
	}
	rel, _ := filepath.Rel(g.root, path)
	// author of first commit touching the file
	out, err := exec.Command("git", "-C", g.root, "log", "--format=%an <%ae>", "--reverse", "--", rel).Output()
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", nil
	}
	// first line
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s, nil
}

func (g *Git) CreationDate(path string) (string, error) {
	if g.disabled {
		return "", nil
	}
	rel, _ := filepath.Rel(g.root, path)
	out, err := exec.Command("git", "-C", g.root, "log", "--format=%ad", "--date=short", "--reverse", "--", rel).Output()
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", nil
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s, nil
}

func (g *Git) LastUpdateDate(path string) (string, error) {
	if g.disabled {
		return "", nil
	}
	rel, _ := filepath.Rel(g.root, path)
	out, err := exec.Command("git", "-C", g.root, "log", "-1", "--format=%ad", "--date=short", "--", rel).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *Git) Touched(ctx context.Context, path string) (bool, error) {
	if g.disabled {
		return true, nil
	}
	rel, _ := filepath.Rel(g.root, path)
	// Check if file differs from HEAD (staged or unstaged) or untracked
	// 1) git status --porcelain
	out, err := exec.CommandContext(ctx, "git", "-C", g.root, "status", "--porcelain", "--", rel).Output()
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return true, nil
	}
	// 2) if tracked but no changes, it's not touched
	// if untracked, status would show it, so here it's clean
	return false, nil
}
