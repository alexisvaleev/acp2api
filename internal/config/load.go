package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/quonaro/acp2api/internal/agent"
)

// Environment variables that override file values.
const (
	EnvAddr       = "ACP2API_ADDR"
	EnvToken      = "ACP2API_TOKEN"
	EnvWorkspace  = "ACP2API_WORKSPACE"
	EnvPermission = "ACP2API_PERMISSION"
	EnvFilesystem = "ACP2API_FILESYSTEM"
)

// Load returns the default configuration merged with the file at path (when
// non-empty) and the environment. It does not validate.
//
// The file is YAML. YAML is a superset of JSON, so an existing JSON config keeps
// working through the same parser, and comments — which JSON does not allow —
// are available in new ones.
//
// Parsing is strict: an unknown key is an error, so a typo — or a key a
// previous version had — stops the process instead of quietly changing what the
// gateway does.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
		expanded, err := expandEnv(string(data))
		if err != nil {
			return Config{}, fmt.Errorf("config: %s: %w", path, err)
		}
		dec := yaml.NewDecoder(strings.NewReader(expanded))
		dec.KnownFields(true)
		// An empty document is not a mistake: a file of nothing but comments
		// still means "use the defaults".
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}

	applyEnv(&cfg)
	cfg.normalise()
	return cfg, nil
}

// applyEnv overlays environment variables onto the configuration.
func applyEnv(cfg *Config) {
	if v := os.Getenv(EnvAddr); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv(EnvToken); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv(EnvWorkspace); v != "" {
		cfg.Workspace = v
	}
	if v := os.Getenv(EnvPermission); v != "" {
		cfg.Permission = v
	}
	if v := os.Getenv(EnvFilesystem); v != "" {
		cfg.Filesystem = v
	}
}

// normalise fills in defaults and cleans paths.
//
// ConversationHeader is deliberately not filled in: an empty value is how a
// deployment disables the mechanism, so refilling it would take away the off
// switch. The default lives in Default(), which a file overlays.
func (c *Config) normalise() {
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.Permission == "" {
		c.Permission = "allow"
	}
	if c.Filesystem == "" {
		c.Filesystem = agent.FilesystemFull
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = int(DefaultRequestTimeout / time.Second)
	}
	if c.SessionTTLSeconds == 0 {
		c.SessionTTLSeconds = int(DefaultSessionTTL / time.Second)
	}
	if c.Workspace != "" {
		c.Workspace = absolute(c.Workspace)
	}
	for i := range c.Agents {
		if c.Agents[i].Workspace != "" {
			c.Agents[i].Workspace = absolute(c.Agents[i].Workspace)
		}
	}
}

// absolute resolves a path against the process working directory, leaving it
// alone when it cannot be resolved.
func absolute(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
