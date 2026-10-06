package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJailPathAllowsInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := resolveWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"a.txt", "./a.txt", "sub/../a.txt"} {
		got, err := jailPath(resolvedRoot, rel)
		if err != nil {
			t.Fatalf("jailPath(%q): %v", rel, err)
		}
		if filepath.Dir(got) != resolvedRoot {
			t.Fatalf("jailPath(%q) = %q, want a path inside %q", rel, got, resolvedRoot)
		}
	}
}

func TestJailPathAllowsNotYetCreatedFile(t *testing.T) {
	root, err := resolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jailPath(root, "new/deep/file.txt"); err != nil {
		t.Fatalf("jailPath on a new file: %v", err)
	}
}

func TestJailPathRejectsEscapes(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := resolveWorkspace(filepath.Join(parent, "ws"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"dotdot":         "../secret.txt",
		"absolute":       filepath.Join(parent, "secret.txt"),
		"deep-dotdot":    "a/b/../../../secret.txt",
		"sibling-prefix": "../ws-other/secret.txt",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := jailPath(root, path); err == nil {
				t.Fatalf("jailPath(%q) = %q, want an error", path, got)
			}
		})
	}
}

func TestJailPathRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := resolveWorkspace(filepath.Join(parent, "ws"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got, err := jailPath(root, "link.txt"); err == nil {
		t.Fatalf("jailPath through a symlink = %q, want an error", got)
	}
}

func TestSliceLines(t *testing.T) {
	content := "l1\nl2\nl3\nl4"
	two, three := 2, 3

	if got := sliceLines(content, nil, nil); got != content {
		t.Fatalf("no window = %q, want the whole content", got)
	}
	if got := sliceLines(content, &two, nil); got != "l2\nl3\nl4" {
		t.Fatalf("from line 2 = %q", got)
	}
	if got := sliceLines(content, &two, &three); got != "l2\nl3\nl4" {
		t.Fatalf("line 2 limit 3 = %q", got)
	}
	one, oneLimit := 1, 1
	if got := sliceLines(content, &one, &oneLimit); got != "l1" {
		t.Fatalf("line 1 limit 1 = %q", got)
	}
}
