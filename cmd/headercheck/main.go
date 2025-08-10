package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/samber/headercheck/internal/config"
	"github.com/samber/headercheck/internal/engine"
	"github.com/samber/headercheck/internal/gitmeta"
)

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	*s = append(*s, parts...)
	return nil
}

func main() {
	var (
		configPaths stringSlice
		fix         bool
		force       bool
		verbose     bool
		templates   stringSlice
		includeRe   string
		excludeRe   string
	)

	parseFlags(&configPaths, &fix, &force, &verbose, &templates, &includeRe, &excludeRe)

	rootAbs := mustGetwd()

	cfg := loadConfigs(rootAbs, configPaths)
	cfg = applyTemplateFlags(rootAbs, cfg, templates, includeRe, excludeRe)

	ctx := context.Background()
	gm := initGit(ctx, rootAbs, verbose)

	rules := compileEngineRules(cfg)

	en := mustNewEngine(rootAbs, rules, force, verbose, gm)

	paths := collectPaths(rootAbs)

	results, err := runEngine(ctx, en, paths, fix)
	if errors.Is(err, context.Canceled) {
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("processing error: %v", err)
	}

	handleResults(rootAbs, results, fix, verbose)
}

func parseFlags(configPaths *stringSlice, fix, force, verbose *bool, templates *stringSlice, includeRe, excludeRe *string) {
	flag.Var(configPaths, "config", "path(s) to .headercheck.yaml; can be repeated")
	flag.BoolVar(fix, "fix", false, "apply fixes: insert or update headers in place")
	flag.BoolVar(force, "force", false, "force processing of non-text/invalid files and print non-blocking warnings")
	flag.BoolVar(verbose, "v", false, "verbose output")
	flag.Var(templates, "template", "additional header template file path(s), comma-separated; can be repeated")
	flag.StringVar(includeRe, "include", "", "regex of file paths to include (overrides config)")
	flag.StringVar(excludeRe, "exclude", "", "regex of file paths to exclude (overrides config)")
	flag.Parse()
}

func mustGetwd() string {
	rootAbs, err := os.Getwd()
	if err != nil {
		log.Fatalf("failed to get current directory: %v", err)
	}
	return rootAbs
}

func loadConfigs(rootAbs string, configPaths []string) config.Config {
	var (
		cfg config.Config
		err error
	)
	if len(configPaths) == 0 {
		cfg, err = config.Load("", rootAbs)
		if err != nil {
			log.Fatalf("config error: %v", err)
		}
		return cfg
	}
	cfg = config.Config{}
	for _, p := range configPaths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(rootAbs, p)
		}
		if _, statErr := os.Stat(abs); statErr != nil {
			log.Fatalf("config file not found: %s", abs)
		}
		c, lerr := config.Load(abs, rootAbs)
		if lerr != nil {
			log.Fatalf("config error for %s: %v", abs, lerr)
		}
		cfg.Templates = append(cfg.Templates, c.Templates...)
	}
	return cfg
}

func applyTemplateFlags(rootAbs string, cfg config.Config, templates []string, includeRe, excludeRe string) config.Config {
	if len(templates) == 0 {
		return cfg
	}
	for _, t := range templates {
		td := config.TemplateDef{Path: t, Include: includeRe, Exclude: excludeRe}
		if !filepath.IsAbs(td.Path) {
			td.Path = filepath.Join(rootAbs, td.Path)
		}
		cfg.Templates = append(cfg.Templates, td)
	}
	return cfg
}

func initGit(ctx context.Context, rootAbs string, verbose bool) *gitmeta.Git {
	gm, err := gitmeta.New(ctx, rootAbs)
	if err != nil {
		if verbose {
			log.Printf("warning: git metadata disabled: %v", err)
		}
		return gitmeta.Disabled()
	}
	return gm
}

func compileEngineRules(cfg config.Config) []engine.TemplateRule {
	var rules []engine.TemplateRule
	for _, t := range cfg.Templates {
		var incRx, excRx *regexp.Regexp
		var err error
		if t.Include == "" {
			t.Include = engine.DefaultIncludeRegex
		}
		incRx, err = regexp.Compile(t.Include)
		if err != nil {
			log.Fatalf("invalid include regex for template %s: %v", t.Path, err)
		}
		if t.Exclude != "" {
			excRx, err = regexp.Compile(t.Exclude)
			if err != nil {
				log.Fatalf("invalid exclude regex for template %s: %v", t.Path, err)
			}
		}
		rules = append(rules, engine.TemplateRule{TemplatePath: t.Path, Include: incRx, Exclude: excRx})
	}
	return rules
}

func mustNewEngine(rootAbs string, rules []engine.TemplateRule, force, verbose bool, gm *gitmeta.Git) *engine.Engine {
	en, err := engine.New(engine.Options{
		Root:       rootAbs,
		Rules:      rules,
		Force:      force,
		Verbose:    verbose,
		Git:        gm,
		RespectGit: true,
	})
	if err != nil {
		log.Fatalf("init error: %v", err)
	}
	return en
}

func collectPaths(rootAbs string) []string {
	// Collect paths from CLI, default to current directory.
	paths := flag.Args()
	if len(paths) == 0 {
		paths = []string{rootAbs}
	}
	for i, p := range paths {
		if !filepath.IsAbs(p) {
			paths[i] = filepath.Join(rootAbs, p)
		}
	}
	return paths
}

func runEngine(ctx context.Context, en *engine.Engine, paths []string, fix bool) ([]engine.FileResult, error) {
	results, err := en.Process(ctx, paths, fix)
	return results, err
}

func handleResults(rootAbs string, results []engine.FileResult, fix, verbose bool) {
	var hadIssues bool
	for _, r := range results {
		if r.Warning != "" {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", r.Path, r.Warning)
		}
		if r.Err != nil {
			// non-fatal per file
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", r.Path, r.Err)
			hadIssues = true
			continue
		}
		if r.Action == engine.ActionNone {
			continue
		}
		if !fix && r.Action != engine.ActionNone {
			// report as linter issue style
			rel, _ := filepath.Rel(rootAbs, r.Path)
			fmt.Printf("%s:1: missing or incorrect header (%s)\n", rel, r.Action)
			hadIssues = true
		} else if fix && verbose {
			fmt.Printf("fixed: %s (%s)\n", r.Path, r.Action)
		}
	}

	if hadIssues && !fix {
		// non-zero exit when in check mode and issues found
		os.Exit(1)
	}
}
