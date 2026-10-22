// Package profiles embeds the distribution templates for clean and full-stack
// modes, plus the content snapshot the integrations engine deploys.
package profiles

import (
	"embed"
	"fmt"
	"io/fs"
)

// EmbeddedProfiles contains embedded profile templates for clean and full-stack modes.
//
//go:embed all:clean all:full
var EmbeddedProfiles embed.FS

// EmbeddedTools is the content snapshot the integrations engine deploys
// (P5-3 T7). It mirrors the repo-level rules/, skills/, plugins/ and hooks/
// trees byte for byte, and tools_sync_test.go fails the build when the two
// drift, so the snapshot is a committed copy rather than a second source of
// truth. See tools/README.md for the regeneration procedure.
//
//go:embed all:tools
var EmbeddedTools embed.FS

// GetToolsFS returns the integrations content snapshot: a filesystem whose
// top-level entries are exactly rules, skills, plugins and hooks, each one
// already shaped the way the engine's layout expects (a feature directory maps
// onto <Gemini config dir>/<feature>).
func GetToolsFS() fs.FS {
	sub, err := fs.Sub(EmbeddedTools, "tools")
	if err != nil {
		// Unreachable: the embedded tree always contains tools/, and the
		// compiler would fail the build first. It exists so the function has no
		// error to leak into every caller.
		panic(fmt.Sprintf("profiles: embedded tools snapshot is unusable: %v", err))
	}
	return sub
}

// GetProfileFS returns a scoped sub-filesystem for the given profile ("clean" or "full").
func GetProfileFS(profileName string) (fs.FS, error) {
	switch profileName {
	case "clean", "default":
		return fs.Sub(EmbeddedProfiles, "clean")
	case "full", "full-stack":
		return fs.Sub(EmbeddedProfiles, "full")
	default:
		return nil, fmt.Errorf("unknown profile: %s", profileName)
	}
}
