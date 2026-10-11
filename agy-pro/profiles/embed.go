// Package profiles embeds the distribution templates for clean and full-stack modes.
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
