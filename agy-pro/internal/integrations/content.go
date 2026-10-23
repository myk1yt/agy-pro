// Content layout and source enumeration for the integrations engine.
//
// The engine is CONTENT-SOURCE-AGNOSTIC (P5-3 orchestration decision b): it is
// handed an fs.FS and a destination map, and it neither knows nor cares whether
// the bytes come from an embedded snapshot or from a runtime tree read. The
// canonical source is the embedded snapshot the CLI passes in
// (profiles.GetToolsFS, which mirrors the repo trees and is guarded by the
// profiles sync gate). The one runtime read that survives is a FALLBACK: a
// caller with no source can still deploy hooks/hooks.json from the installed
// toolkit via ToolkitRoot().
//
// The destination map mirrors the mapping internal/mode already established
// (agy-pro/internal/mode/mode.go:455-474): the path a file has inside the
// source tree IS its path below the Gemini config directory, so
//
//	<source>/rules/AGENTS.md        -> <config>/rules/AGENTS.md
//	<source>/skills/foo/SKILL.md    -> <config>/skills/foo/SKILL.md
//	<source>/plugins/bar/plugin.json-> <config>/plugins/bar/plugin.json
//	<source>/hooks/hooks.json       -> <config>/hooks/hooks.json
//
// The destination root comes from paths.GeminiConfigDir(), which honors
// GEMINI_CONFIG_DIR exactly the way mode does, so every test runs inside a temp
// directory and never touches a real ~/.gemini.
//
// Destination partition (pick record item 1): this engine owns rules, skills,
// plugins and the hooks file. statusLine lives in settings.json and is T5's
// door; mode.Switch keeps agents/references/profile.json/config.json. One owner
// per destination, by construction.
package integrations

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"agypro/internal/paths"
)

// Source directory names. Each is both the directory inside the source tree and
// the directory below the Gemini config dir. They are the same string by
// construction of the mapping above, but they are named separately so a future
// divergence is a one-line change here rather than a hunt through the engine.
const (
	sourceDirRules   = "rules"
	sourceDirSkills  = "skills"
	sourceDirPlugins = "plugins"
	sourceDirHooks   = "hooks"

	// hookSourceFile is the single file the hook feature deploys. The Node-era
	// installer copied the same file (scripts/lib/configure-customizations.js
	// categories), and internal/mode's managed set deliberately leaves hooks
	// alone so this engine can own it.
	hookSourceFile = "hooks.json"
)

// featureSourceDirs is the closed set of features this engine deploys. A
// feature that is absent is not a layout this engine can build: statusline is
// the settings-door feature (T5), and anything else is not a feature at all.
var featureSourceDirs = map[Feature]string{
	FeatureRules:   sourceDirRules,
	FeatureSkills:  sourceDirSkills,
	FeaturePlugins: sourceDirPlugins,
	FeatureHook:    sourceDirHooks,
}

// FeatureLayout is where one feature's content lives in the source tree and
// where it lands below the Gemini config directory.
type FeatureLayout struct {
	// Feature is the feature this layout serves.
	Feature Feature
	// SourceDir is the slash-separated directory inside the source fs.FS.
	SourceDir string
	// SourceFile restricts the feature to one file below SourceDir. It is set
	// for the hook feature ("hooks.json") and empty for the tree features.
	SourceFile string
	// DestDir is the absolute destination directory, i.e.
	// <Gemini config dir>/<SourceDir>.
	DestDir string
}

// SingleFile reports whether the layout deploys exactly one file.
func (l FeatureLayout) SingleFile() bool {
	return l.SourceFile != ""
}

// SourcePath returns the slash path of the layout's single file inside the
// source fs.FS. It is only meaningful when SingleFile reports true.
func (l FeatureLayout) SourcePath() string {
	if l.SourceFile == "" {
		return l.SourceDir
	}
	return path.Join(l.SourceDir, l.SourceFile)
}

// Destination returns the absolute path a source-relative file maps onto.
//
// The mapping is the identity join used by mode (mode.go:462), and it is
// containment-checked here: a relative path is refused unless fs.ValidPath
// accepts it and the joined result stays inside DestDir. A source tree is data,
// so it must not be able to name a destination outside the directory this
// engine owns (CWE-22).
func (l FeatureLayout) Destination(rel string) (string, error) {
	if !fs.ValidPath(rel) || rel == "." {
		return "", fmt.Errorf("source path %q is not a clean forward-slash relative path", rel)
	}
	destination := filepath.Join(l.DestDir, filepath.FromSlash(rel))
	inside, err := filepath.Rel(l.DestDir, destination)
	if err != nil {
		return "", fmt.Errorf("source path %q: %w", rel, err)
	}
	if inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source path %q escapes the %q destination directory", rel, l.DestDir)
	}
	return destination, nil
}

// LayoutFor builds the layout of one feature. The destination root is read from
// paths.GeminiConfigDir() at call time, so GEMINI_CONFIG_DIR moves the whole
// engine in tests and the resolver stays the single authority on the path.
func LayoutFor(feature Feature) (FeatureLayout, error) {
	sourceDir, ok := featureSourceDirs[feature]
	if !ok {
		if feature == FeatureStatusline {
			return FeatureLayout{}, fmt.Errorf("%w: %q is the settings-line door and is not deployed by the content engine",
				ErrNoContentLayout, string(feature))
		}
		return FeatureLayout{}, fmt.Errorf("%w: %q", ErrUnknownFeature, string(feature))
	}
	configDir, err := paths.GeminiConfigDir()
	if err != nil {
		return FeatureLayout{}, fmt.Errorf("layout for %q: %w", string(feature), err)
	}
	layout := FeatureLayout{
		Feature:   feature,
		SourceDir: sourceDir,
		DestDir:   filepath.Join(configDir, sourceDir),
	}
	if feature == FeatureHook {
		layout.SourceFile = hookSourceFile
	}
	return layout, nil
}

// SourceFile is one file the engine would deploy, as read and hashed at plan
// time. Data carries the bytes the plan intends to write, so Apply never has to
// re-read the source: the bytes a plan reports are the bytes a plan writes.
type SourceFile struct {
	// RelPath is the slash path below the layout's SourceDir.
	RelPath string
	// SourcePath is the slash path inside the source fs.FS.
	SourcePath string
	// DestPath is the absolute destination path.
	DestPath string
	// SHA256 is the lowercase hex digest of Data.
	SHA256 string
	// Data is the content read at plan time. For the hook feature it is
	// conditional: the deploy-time rewrite of the source bytes when a launcher
	// resolves and the document verifies (hookrewrite.go), and the unchanged source
	// bytes otherwise (the documented bare fallback, and every document the rewrite
	// declines). Either way the digest here is the digest of what lands on disk,
	// not of what the snapshot carries.
	Data []byte
	// HookCommand is the deploy-time command rewrite's posture record. It is set
	// for the hook feature only and nil for every tree feature.
	HookCommand *HookCommandRewrite
}

// Enumerate reads every file a feature deploys out of source, hashing each one
// and mapping it onto its destination. The result is sorted by RelPath, so a
// plan built twice from the same tree has the same order and the same bytes.
//
// For the hook feature the bytes (and therefore the digest) are what the deploy
// will write, which is the deploy-time rewrite of the command value when a
// launcher resolves and the source bytes otherwise (the bare fallback, or a
// document the rewrite declines: hookrewrite.go). Every other feature deploys its
// source bytes unchanged.
//
// The skip list mirrors the Node deployer the engine replaces
// (scripts/lib/configure-customizations.js syncDirectory): a .git directory, a
// *.bak file suffix, and anything carrying a .bak. infix are not content, and
// deploying them would push an editor's leftovers into the user's config tree.
func Enumerate(feature Feature, source fs.FS) ([]SourceFile, error) {
	layout, err := LayoutFor(feature)
	if err != nil {
		return nil, err
	}
	return enumerateLayout(layout, source)
}

// enumerateLayout is Enumerate against an already-built layout, so a caller
// that resolved the layout once (a plan, a test) does not resolve it twice.
func enumerateLayout(layout FeatureLayout, source fs.FS) ([]SourceFile, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: %q needs a content source", ErrNoContentSource, string(layout.Feature))
	}

	if layout.SingleFile() {
		file, err := readSourceFile(source, layout, layout.SourcePath())
		if err != nil {
			return nil, err
		}
		return []SourceFile{file}, nil
	}

	if _, err := fs.Stat(source, layout.SourceDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: the source tree has no %q directory", ErrSourceUnusable, layout.SourceDir)
		}
		return nil, fmt.Errorf("%w: %q: %v", ErrSourceUnusable, layout.SourceDir, err)
	}

	files := make([]SourceFile, 0, 16)
	walkErr := fs.WalkDir(source, layout.SourceDir, func(sourcePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%w: %q: %v", ErrSourceUnusable, sourcePath, err)
		}
		if entry.IsDir() {
			if sourcePath != layout.SourceDir && skipSourceEntry(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if skipSourceEntry(entry.Name()) {
			return nil
		}
		if !entry.Type().IsRegular() {
			// A symlink or a device node in a source tree is not content: the
			// engine writes regular files, and following a link would let the
			// source name a file outside the tree it was handed.
			return nil
		}
		file, readErr := readSourceFile(source, layout, sourcePath)
		if readErr != nil {
			return readErr
		}
		files = append(files, file)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})
	return files, nil
}

// readSourceFile reads and hashes one file and maps it onto its destination.
//
// The hook file is the one source whose deployed bytes can differ from its source
// bytes: its command value is rewritten to this machine's absolute launcher at read
// time when a launcher resolves and the document verifies (hookrewrite.go). With no
// launcher, or for a document the rewrite declines, the source bytes are what
// deploys. Either way the digest this returns - and therefore every plan, apply,
// manifest entry and status comparison - describes the bytes that land on disk.
func readSourceFile(source fs.FS, layout FeatureLayout, sourcePath string) (SourceFile, error) {
	rel := strings.TrimPrefix(sourcePath, layout.SourceDir+"/")
	if layout.SingleFile() && rel == sourcePath {
		rel = layout.SourceFile
	}
	destination, err := layout.Destination(rel)
	if err != nil {
		return SourceFile{}, fmt.Errorf("%w: %v", ErrSourceUnusable, err)
	}
	data, err := fs.ReadFile(source, sourcePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SourceFile{}, fmt.Errorf("%w: the source tree has no %q", ErrSourceUnusable, sourcePath)
		}
		return SourceFile{}, fmt.Errorf("%w: %q: %v", ErrSourceUnusable, sourcePath, err)
	}
	file := SourceFile{
		RelPath:    rel,
		SourcePath: sourcePath,
		DestPath:   destination,
	}
	if layout.Feature == FeatureHook {
		file.Data, file.HookCommand = hookCommandRewriteFor(data)
	} else {
		file.Data = data
	}
	file.SHA256 = SHA256Hex(file.Data)
	return file, nil
}

// skipSourceEntry reports whether a source entry name is deployer noise rather
// than content.
func skipSourceEntry(name string) bool {
	if name == ".git" {
		return true
	}
	return strings.HasSuffix(name, ".bak") || strings.Contains(name, ".bak.")
}

// OpenFeatureSource returns the fs.FS the engine reads a feature from.
//
// The caller's content source WINS for every feature, the hook included: the
// embedded snapshot (profiles.GetToolsFS, which mirrors the repo trees and is
// guarded by the profiles sync gate) is the canonical deploy source, so a deploy
// reads exactly the bytes the snapshot was built from (P5-3 T7).
//
// The runtime toolkit root is the FALLBACK, and only for the hook: a caller that
// passes no source can still deploy hooks/hooks.json from the installed toolkit,
// which is where a Node-era install keeps it. Tree features have no such runtime
// read - a nil source for rules/skills/plugins is a loud refusal, never an empty
// deployment.
func OpenFeatureSource(feature Feature, content fs.FS) (fs.FS, error) {
	if content != nil {
		return content, nil
	}
	if feature == FeatureHook {
		root, err := ToolkitRoot()
		if err != nil {
			return nil, err
		}
		return os.DirFS(root), nil
	}
	return nil, fmt.Errorf("%w: %q needs the engine's content source", ErrNoContentSource, string(feature))
}

// SHA256Hex returns the lowercase hex SHA-256 of data, the digest form the
// manifest stores and validates.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// liveDestination reports the bytes currently at a destination. present is
// false when nothing is there (the create case). A destination that exists but
// is a directory is a refusal rather than an empty file: the engine writes
// files, and overwriting a directory would be a silent data loss.
func liveDestination(destination string) (sha string, present bool, err error) {
	info, statErr := os.Stat(destination)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("cannot inspect %q: %w", destination, statErr)
	}
	if info.IsDir() {
		return "", false, fmt.Errorf("%q is a directory, not a file the engine can deploy", destination)
	}
	data, readErr := os.ReadFile(destination)
	if readErr != nil {
		return "", false, fmt.Errorf("cannot read %q: %w", destination, readErr)
	}
	return SHA256Hex(data), true, nil
}

// Error sentinels for the content layer.
var (
	// ErrNoContentLayout means a feature has no destination map in this engine.
	ErrNoContentLayout = errors.New("feature has no content layout")
	// ErrNoContentSource means a feature was planned without an fs.FS to read.
	ErrNoContentSource = errors.New("no content source was supplied")
	// ErrSourceUnusable means the source tree is missing or cannot be read as
	// the layout expects. It is never treated as "deploy nothing": a missing
	// tree is a broken installation, not an empty one.
	ErrSourceUnusable = errors.New("content source is unusable")
)
