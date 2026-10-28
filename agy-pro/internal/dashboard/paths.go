// Package dashboard is the read-side client of the dashboard contract the Node
// aggregator publishes. It resolves the artifact directory the Node side owns
// (~/.gemini/antigravity-dashboard/) and decodes the two files that cross the
// process boundary: the atomic payload snapshot (dashboard-data.json) and the
// server port file (dashboard-server.json).
//
// The package performs no exec, no spawn and no network I/O. Node stays the
// sole aggregator: Go only reads what Node wrote, and a snapshot whose schema
// version is behind the published contract is reported as StatusStale instead
// of being rendered as numbers.
package dashboard

import (
	"path/filepath"

	"agypro/internal/paths"
)

const (
	// geminiDirName is the directory agy resolves under the user home. The
	// dashboard artifacts live in a subdirectory of it.
	geminiDirName = ".gemini"

	// DirName is the dashboard artifact directory name under ~/.gemini.
	DirName = "antigravity-dashboard"

	// dataFileName is the atomic payload snapshot written by the Node side
	// (src/html-report.js writeDashboardFiles).
	dataFileName = "dashboard-data.json"

	// serverFileName is the port record / spawn intent file
	// (src/dashboard-link.js writePortFile, writeSpawnIntent).
	serverFileName = "dashboard-server.json"

	// htmlFileName is the server-less static report.
	htmlFileName = "dashboard.html"

	// dirPathShape is the documented shape of the dashboard directory.
	// Diagnostics name this shape, never the resolved absolute path, so no
	// user directory leaks into a message.
	dirPathShape = "~/.gemini/antigravity-dashboard"
)

// homeDir is a seam so tests can point resolution at a temp home. Production
// reads the real user home through paths.UserHome.
var homeDir = paths.UserHome

// dirOverride, when non-empty, replaces the resolved dashboard directory
// outright. Tests set it (and restore it with t.Cleanup) to aim the loaders at
// a fixture tree without touching the user's real artifacts.
var dirOverride string

// Dir resolves the dashboard artifact directory: ~/.gemini/antigravity-dashboard.
func Dir() (string, error) {
	if dirOverride != "" {
		return filepath.Clean(dirOverride), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, geminiDirName, DirName), nil
}

// DataJSONPath resolves the payload snapshot inside the dashboard directory.
func DataJSONPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, dataFileName), nil
}

// ServerJSONPath resolves the server port file inside the dashboard directory.
func ServerJSONPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, serverFileName), nil
}

// HTMLPath resolves the static dashboard report inside the dashboard directory.
func HTMLPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, htmlFileName), nil
}
