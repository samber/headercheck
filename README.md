
# headercheck linter

`headercheck` is a multi-language header linter and fixer. It checks that source files contain a required header and can insert or update it automatically. It is configurable, Git-aware, and compatible with golangci-lint via plugin or by running the CLI directly in CI.

## Features

- **Validate headers**: presence and formatting of file headers
- **Autofix**: insert or update headers in-place (`--fix`)
- **Multiple header templates**: matched at most once per file
- **Binary-safe**: ignore binary files by default; `--force` warns but continues
- **Git-aware variables and change detection**:
  - `%author%`: first committer name of the file
  - `%creation_date%`: date of first commit touching the file (YYYY-MM-DD)
  - `%last_update_date%`: date of last commit touching the file (YYYY-MM-DD)
  - Don’t update headers in fix mode if file hasn’t changed since HEAD
- **Flexible scoping**: include/exclude by regex; defaults to popular source extensions
- **Shebang-aware**: keeps `#!/usr/bin/env ...` on top

## Install

```bash
go install github.com/samber/headercheck/cmd/headercheck@latest
```

## Quick start

1) Create a template at project root: `.header.txt`

```text
/*
Author: %author%
Created: %creation_date%
Last Update: %last_update_date%
Project: My Awesome App
*/
```

2) Run the linter:

```bash
headercheck ./...
# or to apply fixes
headercheck --fix ./...
```

By default, common source files are included. Use `--include`/`--exclude` to refine.

## Examples

The `examples/` directory contains sample projects showing different configurations:

- `examples/basic-go`: minimal Go project with a single template for `.go` files.
  - Run: `headercheck examples/basic-go` (or `--fix`)
- `examples/mixed`: mixed repository with code, scripts, and YAML configs using three templates (`.code.header.txt`, `.scripts.header.txt`, `.yaml.header.txt`) and per-template include/exclude rules.
  - Run: `headercheck examples/mixed` (or `--fix`)

You can copy one of these into your project as a starting point and adapt the templates.

## Configuration (.headercheck.yaml)

At the repository root (or provide `--config path`):

```yaml
# .headercheck.yaml
# List of template files (absolute or relative to repo root)
templates:
  - path: .header.txt
    include: (?i)\.(go|js|ts|tsx|jsx|rs|py|rb|php|java|kt|c|h|cpp|hpp|m|mm|swift|scala|sql|proto)$
    exclude: vendor/|^third_party/
  - path: .scripts.header.txt
    include: (?i)\.(sh|bash|zsh|ps1)$
```

CLI flags override config values:

- `--config path`: path to `headercheck.yaml`
- `--template path[,path...]`: add more templates (applies default include/exclude)
- `--include regex`, `--exclude regex`: default include/exclude applied to templates lacking their own
- `--fix`: apply changes
- `--force`: process invalid/binary files with a warning
- `-v`: verbose

## golangci-lint integration

Two supported paths:

### Module Plugin System (recommended)

Create `.custom-gcl.yml` in your repository that pulls headercheck as a plugin:

```yaml
version: v2.0.0
plugins:
  - module: 'github.com/samber/headercheck'
    # version: v0.1.0 # pin your version
```

Build the custom binary:

```bash
golangci-lint custom
# produces ./custom-gcl
```

Then in `.golangci.yml` enable the linter:

```yaml
version: "2"
linters:
  enable:
    - headercheck
linters-settings:
  custom:
    headercheck:
      description: Checks and fixes file headers
      original-url: github.com/samber/headercheck
      settings:
        template: .header.txt
```

Note: Depending on the GolangCI plugin strategy, you may run the `headercheck` CLI as a separate CI step, which is often simpler when linting non-Go files.

### Go Plugin System

Alternatively build a `.so` plugin (requires CGO and exact dependency versions). See `plugin/headercheck` and GolangCI docs. The CLI remains the primary supported interface.

## Behavior

- **Check mode**: Reports files missing or with a non-matching header. Variable differences (author/dates) are ignored to reduce churn.
- **Fix mode**: Inserts header if missing, or replaces the existing header with a fully rendered one (author/dates updated). Skips files that were not modified in Git (HEAD clean).
- **Binary files**: Skipped; with `--force` we print a non-blocking warning.
- **Multiple templates**: We try to match any; at most one header is inserted per file.
