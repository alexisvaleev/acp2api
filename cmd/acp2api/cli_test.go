package main

import (
	"testing"

	"github.com/quonaro/lota/engine"
)

func TestCLIConfig(t *testing.T) {
	cfg, err := engine.LoadConfig(cliYAML)
	if err != nil {
		t.Fatalf("embedded cli.yml does not parse: %v", err)
	}

	have := make(map[string]bool)
	for _, name := range cfg.GetAllCommandNames() {
		have[name] = true
	}
	for _, want := range []string{"serve", "version"} {
		if !have[want] {
			t.Errorf("command %q missing from cli.yml (have %v)", want, cfg.GetAllCommandNames())
		}
	}
}
