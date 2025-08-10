package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"

	"github.com/samber/headercheck/internal/engine"
	"github.com/samber/headercheck/internal/gitmeta"
	"golang.org/x/tools/go/analysis"
)

// Plugin configuration structure expected from golangci-lint custom settings.
type pluginConfig struct {
	Templates []struct {
		Path    string `mapstructure:"path" yaml:"path"`
		Include string `mapstructure:"include" yaml:"include"`
		Exclude string `mapstructure:"exclude" yaml:"exclude"`
	} `mapstructure:"templates" yaml:"templates"`
}

// New implements golangci-lint plugin entrypoint.
func New(conf any) ([]*analysis.Analyzer, error) { //nolint: revive
	cfg := parseConfig(conf)
	root := resolveRoot()
	normalizeTemplatePathsAndDefaults(root, &cfg)
	gm := gitOrDisabled(root)
	rules := compileRules(cfg)
	a := buildAnalyzer(root, gm, rules)
	return []*analysis.Analyzer{a}, nil
}

func parseConfig(conf any) pluginConfig {
	out := pluginConfig{}
	if conf == nil {
		return out
	}
	// conf is typically map[string]any
	m, ok := conf.(map[string]any)
	if !ok {
		return out
	}
	if v, ok := m["templates"]; ok {
		if list, ok := v.([]any); ok {
			for _, e := range list {
				switch t := e.(type) {
				case string:
					out.Templates = append(out.Templates, struct {
						Path    string `mapstructure:"path" yaml:"path"`
						Include string `mapstructure:"include" yaml:"include"`
						Exclude string `mapstructure:"exclude" yaml:"exclude"`
					}{Path: t})
				case map[string]any:
					var td struct {
						Path    string `mapstructure:"path" yaml:"path"`
						Include string `mapstructure:"include" yaml:"include"`
						Exclude string `mapstructure:"exclude" yaml:"exclude"`
					}
					if s, ok := t["path"].(string); ok {
						td.Path = s
					}
					if s, ok := t["include"].(string); ok {
						td.Include = s
					}
					if s, ok := t["exclude"].(string); ok {
						td.Exclude = s
					}
					if td.Path != "" {
						out.Templates = append(out.Templates, td)
					}
				}
			}
		}
	}
	return out
}

// --- helpers ---

func resolveRoot() string {
	root, _ := os.Getwd()
	return root
}

func normalizeTemplatePathsAndDefaults(root string, cfg *pluginConfig) {
	for i, t := range cfg.Templates {
		if !filepath.IsAbs(t.Path) {
			cfg.Templates[i].Path = filepath.Join(root, t.Path)
		}
		if cfg.Templates[i].Include == "" {
			cfg.Templates[i].Include = engine.DefaultIncludeRegex
		}
	}
}

func gitOrDisabled(root string) *gitmeta.Git {
	gm, err := gitmeta.New(context.Background(), root)
	if err != nil {
		return gitmeta.Disabled()
	}
	return gm
}

func compileRules(cfg pluginConfig) []engine.TemplateRule {
	var rules []engine.TemplateRule
	for _, t := range cfg.Templates {
		incRx := regexp.MustCompile(t.Include)
		var excRx *regexp.Regexp
		if t.Exclude != "" {
			excRx = regexp.MustCompile(t.Exclude)
		}
		rules = append(rules, engine.TemplateRule{TemplatePath: t.Path, Include: incRx, Exclude: excRx})
	}
	return rules
}

func buildAnalyzer(root string, gm *gitmeta.Git, rules []engine.TemplateRule) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "headercheck",
		Doc:  "checks presence of file headers in Go files",
		Run: func(pass *analysis.Pass) (interface{}, error) {
			for _, f := range pass.Files {
				filePath := pass.Fset.File(f.Pos()).Name()
				content, err := os.ReadFile(filePath)
				if err != nil {
					continue
				}
				en, _ := engine.New(engine.Options{Root: root, Rules: rules, Git: gm})
				if en == nil {
					continue
				}
				if !en.AcceptsPath(filePath) {
					// no template applies to this file
					continue
				}
				rendered := en.RenderTemplatesForFiltered(filePath)
				if len(rendered) == 0 {
					continue
				}
				cur, hs, he := engine.DetectHeaderBlock(content)
				_ = hs
				_ = he
				matchedIdx := -1
				for i, tmpl := range rendered {
					if engine.HeaderSemanticallyMatches(cur, tmpl) {
						matchedIdx = i
						break
					}
				}
				if matchedIdx >= 0 {
					continue
				}
				insertPos := f.Package
				newHeader := []byte{}
				if len(rendered) > 0 {
					newHeader = rendered[0]
				}
				edits := []analysis.TextEdit{{
					Pos:     insertPos,
					End:     insertPos,
					NewText: append(append([]byte{}, newHeader...), '\n'),
				}}
				pass.Report(analysis.Diagnostic{
					Pos:     insertPos,
					Message: "missing or incorrect file header",
					SuggestedFixes: []analysis.SuggestedFix{{
						Message:   "Add header",
						TextEdits: edits,
					}},
				})
				_ = bytes.MinRead
			}
			return nil, nil
		},
	}
}
