package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveWorkspace normalises the workspace root to a real, absolute path.
func resolveWorkspace(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("client: workspace is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("client: resolve workspace: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("client: resolve workspace symlinks: %w", err)
	}
	return real, nil
}

// jailPath resolves a path and guarantees it stays inside root.
//
// Symlinks are resolved on the deepest existing ancestor before the containment
// check, so a symlink pointing outside the workspace cannot be used to escape
// it — neither for reading an existing file nor for creating a new one.
func jailPath(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("client: empty path")
	}

	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, path)
	}
	abs = filepath.Clean(abs)

	real, err := evalExisting(abs)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(root, real)
	if err != nil {
		return "", fmt.Errorf("client: relativise %q: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("client: path %q escapes the workspace", path)
	}
	return real, nil
}

// evalExisting resolves symlinks for the deepest existing ancestor of p and
// re-appends the non-existent remainder, so a path that is about to be created
// is still checked against the real location of its parent.
func evalExisting(p string) (string, error) {
	rest := ""
	cur := p
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return real, nil
			}
			return filepath.Join(real, rest), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("client: resolve %q: %w", p, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("client: resolve %q: no existing ancestor", p)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// sliceLines applies the ACP line/limit window. Line is 1-based and inclusive.
func sliceLines(content string, line, limit *int) string {
	if line == nil && limit == nil {
		return content
	}
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
	}
	if start > len(lines) {
		return ""
	}
	end := len(lines)
	if limit != nil && *limit >= 0 {
		if start+*limit < end {
			end = start + *limit
		}
	}
	return strings.Join(lines[start:end], "\n")
}
