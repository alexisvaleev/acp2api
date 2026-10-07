package client

import "fmt"

// Filesystem is the filesystem surface a Handler exposes to an agent.
//
// The three values nest: none implies readonly. The zero value is full, so a
// caller that never mentions the filesystem gets what the gateway has always
// given. The values are the same strings the configuration and the agent
// capabilities use; this package keeps its own copy so the security boundary
// does not depend on the package it is defending against.
type Filesystem string

const (
	// FilesystemFull serves reads and writes.
	FilesystemFull Filesystem = "full"
	// FilesystemReadOnly serves reads and refuses writes.
	FilesystemReadOnly Filesystem = "readonly"
	// FilesystemNone serves neither, so the agent has no filesystem at all.
	FilesystemNone Filesystem = "none"
)

// ParseFilesystem maps a config value to a Filesystem.
func ParseFilesystem(name string) (Filesystem, error) {
	return Filesystem(name).normalise()
}

// normalise validates the mode and maps the zero value to full.
//
// An unrecognised value is an error rather than a silent default: falling back
// to full would grant the filesystem the operator was trying to take away.
func (f Filesystem) normalise() (Filesystem, error) {
	switch f {
	case "":
		return FilesystemFull, nil
	case FilesystemFull, FilesystemReadOnly, FilesystemNone:
		return f, nil
	default:
		return "", fmt.Errorf("client: unknown filesystem mode %q (want \"full\", \"readonly\" or \"none\")", f)
	}
}

// reads reports whether fs/read_text_file is served.
func (f Filesystem) reads() bool { return f != FilesystemNone }

// writes reports whether fs/write_text_file is served.
func (f Filesystem) writes() bool { return f == FilesystemFull }
