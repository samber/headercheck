package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// TemplateDef represents a single template configuration with optional include/exclude.
type TemplateDef struct {
	Path    string `yaml:"path"`
	Include string `yaml:"include"`
	Exclude string `yaml:"exclude"`
}

// Config represents headercheck configuration.
// `templates` can be either a list of strings or a list of objects {path, include, exclude}.
type Config struct {
	Templates []TemplateDef `yaml:"templates"`
	// Legacy/global defaults (optional): applied to templates without include/exclude
	Include string `yaml:"include"`
	Exclude string `yaml:"exclude"`
}

// Load loads configuration from explicit path or common defaults.
func Load(explicitPath string, root string) (Config, error) {
	// Defaults
	cfg := Config{
		Templates: []TemplateDef{{Path: filepath.Join(root, ".header.txt")}},
	}

	// Try load config file
	var candidates []string
	if explicitPath != "" {
		if !filepath.IsAbs(explicitPath) {
			explicitPath = filepath.Join(root, explicitPath)
		}
		candidates = []string{explicitPath}
	} else {
		candidates = []string{
			filepath.Join(root, ".headercheck.yaml"),
			filepath.Join(root, ".headercheck.yml"),
			filepath.Join(root, "headercheck.yaml"),
			filepath.Join(root, "headercheck.yml"),
		}
	}

	var loadedPath string
	var raw map[string]interface{}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return cfg, fmt.Errorf("read config %s: %w", p, err)
		}
		if err := yaml.Unmarshal(b, &raw); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", p, err)
		}
		loadedPath = p
		break
	}

	if loadedPath != "" {
		// Parse templates with flexible types
		if tv, ok := raw["templates"]; ok {
			var defs []TemplateDef
			switch ts := tv.(type) {
			case []interface{}:
				for _, it := range ts {
					switch v := it.(type) {
					case string:
						defs = append(defs, TemplateDef{Path: v})
					case map[string]interface{}:
						def := TemplateDef{}
						if p, ok := v["path"].(string); ok {
							def.Path = p
						}
						if inc, ok := v["include"].(string); ok {
							def.Include = inc
						}
						if exc, ok := v["exclude"].(string); ok {
							def.Exclude = exc
						}
						if strings.TrimSpace(def.Path) != "" {
							defs = append(defs, def)
						}
					}
				}
			}
			if len(defs) > 0 {
				cfg.Templates = defs
			}
		}
		if inc, ok := raw["include"].(string); ok && strings.TrimSpace(inc) != "" {
			cfg.Include = inc
		}
		if exc, ok := raw["exclude"].(string); ok && strings.TrimSpace(exc) != "" {
			cfg.Exclude = exc
		}
	}

	// Normalize template paths and apply defaults
	for i := range cfg.Templates {
		t := cfg.Templates[i]
		if !filepath.IsAbs(t.Path) {
			t.Path = filepath.Join(root, t.Path)
		}
		if t.Include == "" {
			t.Include = cfg.Include
		}
		if t.Exclude == "" {
			t.Exclude = cfg.Exclude
		}
		cfg.Templates[i] = t
	}

	// Remove empty template entries.
	var filtered []TemplateDef
	for _, t := range cfg.Templates {
		if strings.TrimSpace(t.Path) != "" {
			filtered = append(filtered, t)
		}
	}
	cfg.Templates = filtered

	return cfg, nil
}
