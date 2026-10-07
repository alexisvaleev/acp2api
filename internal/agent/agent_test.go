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

func TestFilesystemReadOnlyWithholdsTheWriteCapability(t *testing.T) {
	caps := (Agent{ID: "x", Filesystem: FilesystemReadOnly}).Capabilities()
	fs, ok := caps["fs"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.fs missing: %#v", caps)
	}
	if fs["readTextFile"] != true {
		t.Fatalf("read should stay: %#v", fs)
	}
	// Advertising a capability we then refuse would be a lie, and the agent
	// would spend a turn discovering it.
	if fs["writeTextFile"] != false {
		t.Fatalf("write must be withheld, got %#v", fs)
	}
}

// TestFilesystemNoneAdvertisesNothing is the provider-style mode: the agent is
// told the client has no filesystem, so it answers rather than looking around.
func TestFilesystemNoneAdvertisesNothing(t *testing.T) {
	caps := (Agent{ID: "x", Filesystem: FilesystemNone}).Capabilities()
	fs, ok := caps["fs"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.fs missing: %#v", caps)
	}
	if fs["readTextFile"] != false || fs["writeTextFile"] != false {
		t.Fatalf("no filesystem should be advertised, got %#v", fs)
	}
}

func TestExplicitCapabilitiesWinOverFilesystem(t *testing.T) {
	// An operator who sets capabilities explicitly meant it.
	explicit := map[string]any{"fs": map[string]any{"readTextFile": true, "writeTextFile": true}}
	caps := (Agent{ID: "x", Filesystem: FilesystemNone, ClientCapabilities: explicit}).Capabilities()
	if caps["fs"].(map[string]any)["writeTextFile"] != true {
		t.Fatalf("explicit capabilities were overridden: %#v", caps)
	}
}

func TestFilesystemModeDefaultsToFull(t *testing.T) {
	if got := (Agent{ID: "x"}).FilesystemMode(); got != FilesystemFull {
		t.Fatalf("FilesystemMode() = %q, want %q", got, FilesystemFull)
	}
}

// TestUnknownFilesystemModeFailsClosed: a typo in the mode must not hand the
// agent the filesystem. Configuration validation rejects the value first; this
// is the second line of defence, and the one that holds for library use.
func TestUnknownFilesystemModeFailsClosed(t *testing.T) {
	if got := (Agent{ID: "x", Filesystem: "read-onyl"}).FilesystemMode(); got != FilesystemNone {
		t.Fatalf("FilesystemMode() = %q, want %q", got, FilesystemNone)
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
