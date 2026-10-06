package agent

import "testing"

func TestBuiltinRegistryListsAgents(t *testing.T) {
	r := BuiltinRegistry()
	got := r.List()
	if len(got) != len(Builtins()) {
		t.Fatalf("listed %d agents, want %d", len(got), len(Builtins()))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Fatalf("agents are not sorted: %q before %q", got[i-1].ID, got[i].ID)
		}
	}
}

func TestResolve(t *testing.T) {
	r := BuiltinRegistry()

	tests := []struct {
		modelID   string
		wantAgent string
		wantModel string
		wantErr   bool
	}{
		{modelID: "devin", wantAgent: "devin"},
		{modelID: "devin/opus", wantAgent: "devin", wantModel: "opus"},
		{modelID: "opencode/plan/build", wantAgent: "opencode", wantModel: "plan/build"},
		{modelID: "", wantErr: true},
		{modelID: "nope", wantErr: true},
		{modelID: "nope/x", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.modelID, func(t *testing.T) {
			a, model, err := r.Resolve(tc.modelID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) succeeded, want error", tc.modelID)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.modelID, err)
			}
			if a.ID != tc.wantAgent {
				t.Fatalf("agent = %q, want %q", a.ID, tc.wantAgent)
			}
			if model != tc.wantModel {
				t.Fatalf("model = %q, want %q", model, tc.wantModel)
			}
		})
	}
}

func TestRegisterOverridesWithoutDuplicating(t *testing.T) {
	r := NewRegistry(Agent{ID: "x", Command: "x"})
	r.Register(Agent{ID: "x", Command: "x-custom"})

	if n := len(r.List()); n != 1 {
		t.Fatalf("listed %d agents, want 1", n)
	}
	got, ok := r.Get("x")
	if !ok || got.Command != "x-custom" {
		t.Fatalf("Get(x) = %+v, ok=%v", got, ok)
	}
}

func TestCapabilitiesDefaultAdvertisesFsOnly(t *testing.T) {
	caps := (Agent{ID: "x"}).Capabilities()
	fs, ok := caps["fs"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.fs missing: %#v", caps)
	}
	if fs["readTextFile"] != true || fs["writeTextFile"] != true {
		t.Fatalf("fs capabilities = %#v", fs)
	}
	if _, hasTerminal := caps["terminal"]; hasTerminal {
		t.Fatal("terminal must not be advertised by default")
	}
}
