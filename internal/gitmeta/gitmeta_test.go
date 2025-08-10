package gitmeta

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
)

func TestDisabled_AllowsCalls(t *testing.T) {
	g := Disabled()
	if a, _ := g.Author("x"); a != "" {
		t.Fatalf("author: %q", a)
	}
	if c, _ := g.CreationDate("x"); c != "" {
		t.Fatalf("creation: %q", c)
	}
	if u, _ := g.LastUpdateDate("x"); u != "" {
		t.Fatalf("update: %q", u)
	}
	touched, _ := g.Touched(context.Background(), "x")
	if !touched {
		t.Fatalf("disabled should treat files as touched")
	}
}

func TestNew_FailsOutsideRepo(t *testing.T) {
	// Skip on systems without git in PATH to avoid false failures
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Use a temp dir not initialized as git repo
	dir := t.TempDir()
	_, err := New(context.Background(), dir)
	if err == nil {
		t.Fatalf("expected error outside repo")
	}
}

func TestIntegration_OnlyWhenGitAvailable(t *testing.T) {
	// Light sanity check: create a repo and a file, ensure Touched works.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	if runtime.GOOS == "windows" { // keep CI simple
		t.Skip("skip on windows")
	}
	dir := t.TempDir()
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, string(out))
		}
	}
	run("git", "init", "-b", "main")
	run("git", "config", "user.email", "you@example.com")
	run("git", "config", "user.name", "Your Name")
	// create file
	run("bash", "-c", "echo hi > a.txt")
	run("git", "add", "a.txt")
	run("git", "-c", "commit.gpgsign=false", "commit", "-m", "init")

	g, err := New(context.Background(), dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	touched, err := g.Touched(context.Background(), dir+"/a.txt")
	if err != nil {
		t.Fatalf("touched err: %v", err)
	}
	if touched {
		t.Fatalf("expected not touched after commit")
	}
}
