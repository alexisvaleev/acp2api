package session

import (
	"strings"
	"testing"
)

// TestBuildEnvOverrideWins guards a subtle bug: appending a second variable
// leaves the inherited one first in environ, and getenv returns the first
// match, so the override would be lost without a word.
func TestBuildEnvOverrideWins(t *testing.T) {
	t.Setenv("ACP2API_TEST_OVERRIDE", "inherited")

	env := buildEnv(map[string]string{"ACP2API_TEST_OVERRIDE": "overridden"})

	var seen []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "ACP2API_TEST_OVERRIDE=") {
			seen = append(seen, entry)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("expected exactly one entry, got %v", seen)
	}
	if seen[0] != "ACP2API_TEST_OVERRIDE=overridden" {
		t.Fatalf("entry = %q, the override should win", seen[0])
	}
}

func TestBuildEnvLayersLaterWins(t *testing.T) {
	env := buildEnv(
		map[string]string{"LAYER": "first", "ONLY_FIRST": "a"},
		map[string]string{"LAYER": "second"},
	)

	values := map[string]string{}
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	if values["LAYER"] != "second" {
		t.Fatalf("LAYER = %q, want the last layer", values["LAYER"])
	}
	if values["ONLY_FIRST"] != "a" {
		t.Fatalf("ONLY_FIRST = %q, want it preserved", values["ONLY_FIRST"])
	}
}

func TestBuildEnvKeepsTheInheritedEnvironment(t *testing.T) {
	t.Setenv("ACP2API_TEST_INHERITED", "kept")

	env := buildEnv(map[string]string{"ADDED": "yes"})
	joined := strings.Join(env, "\n")

	for _, want := range []string{"ACP2API_TEST_INHERITED=kept", "ADDED=yes"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment is missing %q", want)
		}
	}
}

func TestBuildEnvWithNoLayersIsTheProcessEnvironment(t *testing.T) {
	env := buildEnv()
	if len(env) == 0 {
		t.Fatal("expected the inherited environment")
	}
	for _, entry := range env {
		if !strings.Contains(entry, "=") {
			t.Fatalf("malformed entry %q", entry)
		}
	}
}
