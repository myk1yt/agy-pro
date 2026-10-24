package agenttools

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ResolveSafePath validates that target resolves to a location within root.
// It guards against directory traversal attacks (e.g. '../', absolute paths outside root,
// or symlinks pointing outside root).
func ResolveSafePath(root, target string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("failed to resolve root directory: %w", err)
	}

	var targetAbs string
	if filepath.IsAbs(target) {
		targetAbs = filepath.Clean(target)
	} else {
		targetAbs = filepath.Clean(filepath.Join(rootAbs, target))
	}

	// Traversal check via filepath.Rel
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path %q escapes root directory %q", target, rootAbs)
	}

	// Symlink escape check: evaluate existing path or closest existing ancestor
	checkPath := targetAbs
	for {
		if _, err := os.Lstat(checkPath); err == nil {
			eval, err := filepath.EvalSymlinks(checkPath)
			if err == nil {
				evalRel, err := filepath.Rel(rootAbs, eval)
				if err != nil || evalRel == ".." || strings.HasPrefix(evalRel, ".."+string(filepath.Separator)) {
					return "", fmt.Errorf("access denied: symlink %q resolves outside root directory: %q", target, eval)
				}
			}
			break
		}
		parent := filepath.Dir(checkPath)
		if parent == checkPath {
			break
		}
		checkPath = parent
	}

	return targetAbs, nil
}

// protectedWriteNames are VCS metadata path components that model-driven
// write tools must never touch (M-2 / house rule §5): a planted
// .git/hooks/pre-commit or a rewritten .git/config turns one prompt-injected
// write into code execution on the user's next git invocation. Keys are
// lowercase; comparison is case-insensitive so Windows case-variant aliases
// (".GIT") are covered on every host.
var protectedWriteNames = map[string]bool{
	".git":           true,
	".github":        true,
	".gitattributes": true,
	".gitconfig":     true,
	".gitignore":     true,
	".gitmodules":    true,
	".svn":           true,
	".hg":            true,
	".bzr":           true,
}

// governanceWriteNames are the basenames and directory components of the
// governance and credential chain: the files and directories that configure
// the agent system itself (agent rules, provider registry, per-run state,
// integration and tool settings). A seat able to rewrite one of them turns a
// single prompt-injected write into a standing change to the operator's
// configuration, which outlives the run and every future run. Keys are
// lowercase and compared case-insensitively, exactly like protectedWriteNames.
// Only the write tools consult this set: reads and searches stay unaffected.
//
// The "profiles/tools/rules" path named in the denylist needs no entry of its
// own: "rules" is the last component of that path, so the component arm below
// already refuses it at any depth.
var governanceWriteNames = map[string]bool{
	"agents.md":           true,
	"gemini.md":           true,
	"claude.md":           true,
	"providers.json":      true,
	"state.json":          true,
	"state-fanout.json":   true,
	"fanout-presets.json": true,
	"integrations.json":   true,
	"settings.json":       true,
	// directory components
	"rules":    true,
	".agy-pro": true,
	".gemini":  true,
}

// governanceSiblingPrefix marks the sibling aliases of the provider registry
// (providers.json.bak, providers.json.tmp, ...): a leftover copy next to the
// live registry is the same escalation step as the registry itself, because
// operators and repair paths restore from it.
const governanceSiblingPrefix = "providers.json."

// governanceDocSuffixes are the documentation extensions that keep a
// "providers.json.<ext>" name WRITABLE: that file is prose ABOUT the registry,
// not a registry alias ("docs/providers.json.md" is a documented look-alike
// that must stay writable). The sibling-alias arm refuses every other suffix.
var governanceDocSuffixes = map[string]bool{
	"md": true, "markdown": true, "txt": true,
	"rst": true, "adoc": true, "asciidoc": true,
	"html": true, "htm": true,
}

// governanceRefusal reports whether the root-relative slash path rel addresses
// a governance/credential target. It returns the offending component as the
// reason plus refuse, or ("", false) when the path is clear. The check is
// component-based, so a protected name is caught at any depth
// ("sub/.gemini/settings.json" too), while names that merely contain a
// protected substring ("docs/agents-notes.md", "docs/providers.json.md") stay
// writable.
//
// The compared key is the FOLDED STREAM BASE of the canonical component
// (FIX-D1a): the $UpCase fold closes the spelling class (win32NameFold), and
// the base before a ':' is the file the OS actually opens, so
// "AGENTS.md:evil" and "AGENTS.md::$DATA" are the same governed name as
// "AGENTS.md". The map keys stay lowercase, so this one line is the whole fold.
func governanceRefusal(rel string) (string, bool) {
	for _, comp := range strings.Split(rel, "/") {
		if comp == "" {
			continue
		}
		lower := win32NameFold(streamBaseOf(canonicalWriteComponent(comp)))
		if governanceWriteNames[lower] {
			return comp, true
		}
		if strings.HasPrefix(lower, governanceSiblingPrefix) {
			if !governanceDocSuffixes[lower[len(governanceSiblingPrefix):]] {
				return comp, true
			}
		}
	}
	return "", false
}

// refuseProtectedWrite rejects writes into the VCS metadata and the governance
// chain relative to the workspace root. It runs AFTER ResolveSafePath (which
// already refuses symlink escapes outside the root); this layer closes the
// in-root part of the persistence chain. The check is component-based on the
// root-relative path, so protected names are refused at any depth
// ("sub/.git/config" too), while names that merely contain a protected
// substring ("docs/.github-notes.md") stay writable.
//
// The resolved-directory-chain arm also checks BOTH denylists through
// resolvedDirChainRelPath: ResolveSafePath accepts in-root links, so a link
// "rules -> .agy-pro" would otherwise let "link/registry-keys.v2.json" land
// inside the protected directory under a clear-looking lexical name, and a link
// ".git"-aliased ("link -> .git") would let "link/hooks/pre-commit" plant a git
// hook (RCE at the operator's next commit). Both classes were needed: the
// lexical arm above matches names, and the link carries none of them; the 8.3
// expansion arm cannot close it either, because GetLongPathNameW does not
// resolve link components. Only the directory chain is resolved, never the
// final component: the atomic temp+rename replaces a file symlink instead of
// writing through it, so the link target's content is never at risk (pinned by
// TestWriteThroughInRootSymlinkCannotTouchHook).
//
// A chain that cannot be resolved at all is a REFUSAL, not a pass (FIX-F1,
// R5-2): the exported predicate this arm shares with the rollback
// (ResolvedDirChainRefusal) fails closed on the same error, and a target whose
// directory chain cannot be described is not one this guard may allow.
func refuseProtectedWrite(root, safePath string) error {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("failed to resolve root directory: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, safePath)
	if err != nil {
		// Unrelatable to the root: fail closed.
		return fmt.Errorf("access denied: cannot verify %q is outside protected VCS metadata", safePath)
	}
	relSlash := filepath.ToSlash(rel)
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		if protectedWriteNames[win32NameFold(streamBaseOf(canonicalWriteComponent(comp)))] {
			return fmt.Errorf("access denied: %q is protected VCS metadata (%s); write_file/replace_in_file refuse it", relSlash, comp)
		}
	}
	if err := canonicalWriteRefusal(rootAbs, safePath, relSlash); err != nil {
		return err
	}
	verdict, err := resolvedDirChain(rootAbs, safePath)
	if err != nil {
		return fmt.Errorf("access denied: cannot resolve the directory chain %q traverses (%v); write_file/replace_in_file refuse it", relSlash, err)
	}
	switch verdict.class {
	case resolvedChainGovernance:
		return governanceWriteRefusal(verdict.realRel, verdict.comp)
	case resolvedChainVCS:
		return resolvedVCSWriteRefusal(relSlash, verdict.realRel, verdict.comp)
	}
	return nil
}

// resolvedChainClass says which denylist a resolved-directory-chain verdict
// matched, so both callers can render their own refusal for the right class.
type resolvedChainClass int

const (
	resolvedChainClear resolvedChainClass = iota
	resolvedChainGovernance
	resolvedChainVCS
)

// resolvedChainVerdict is the classified outcome of the resolved-directory-chain
// walk the write guard and the exported ResolvedDirChainRefusal share.
// realRel is the resolved ROOT-RELATIVE slash path with the final component
// verbatim; dir is its directory part ("" when that part is the root itself);
// comp is the offending component when class is not clear.
type resolvedChainVerdict struct {
	realRel string
	dir     string
	comp    string
	class   resolvedChainClass
}

// resolvedDirChain walks the directory chain of absTarget (resolvedDirChainRelPath,
// runner.go) and judges it against BOTH denylists in the order the guard tests
// them: governance/credential first, then VCS metadata. One implementation, so
// the write arm and the exported predicate cannot drift apart (R5-1: they did,
// and the write arm was the one missing the VCS half).
//
// The returned error is the walk's own error, unchanged: every caller fails
// closed on it.
func resolvedDirChain(rootAbs, absTarget string) (resolvedChainVerdict, error) {
	var v resolvedChainVerdict
	realRel, err := resolvedDirChainRelPath(rootAbs, absTarget)
	if err != nil {
		return v, err
	}
	v.realRel = filepath.ToSlash(realRel)
	dir := path.Dir(v.realRel)
	if dir == "." || dir == "/" || dir == "" {
		return v, nil
	}
	v.dir = dir
	if comp, refuse := governanceRefusal(dir); refuse {
		v.comp, v.class = comp, resolvedChainGovernance
		return v, nil
	}
	if comp, refuse := VCSMetadataRefusal(dir); refuse {
		v.comp, v.class = comp, resolvedChainVCS
		return v, nil
	}
	return v, nil
}

// governanceWriteRefusal renders the error-shaped refusal the model sees and
// the tool log persists. rel names the offending path (the resolved one when
// the link chain is what hit the denylist) and comp the component that matched.
// The message carries no credential value, so the existing registry redactor
// leaves it byte-identical in the log.
func governanceWriteRefusal(rel, comp string) error {
	return fmt.Errorf("access denied: %q is a governance/credential file (%s) that configures the agent system itself; write_file/replace_in_file refuse it", rel, comp)
}

// resolvedVCSWriteRefusal is the VCS-metadata half of the same arm (FIX-F1,
// R5-1): the WRITTEN path carries no protected component, but the directory
// chain it traverses resolves into one. It names both paths, because the model
// authored the first and the write would land in the second, and keeps the
// class phrase of the lexical refusal ("VCS metadata") so both shapes read as
// one rule.
func resolvedVCSWriteRefusal(rel, resolvedRel, comp string) error {
	return fmt.Errorf("access denied: %q resolves to %q, which is protected VCS metadata (%s); write_file/replace_in_file refuse it", rel, resolvedRel, comp)
}

// PathInScope reports whether the root-relative slash path rel is inside the
// unit scope patterns. It is the B3 per-unit working boundary for the
// path-reading tools (read_file / list_dir / grep / glob).
//
// Rule (deterministic, no "**"): the path is split into "/"-separated
// components and compared component by component against each pattern with
// path.Match semantics, which never crosses a separator. A pattern may be
// SHORTER than the path, in which case it matches as a directory prefix
// ("src/foo" covers "src/foo/bar.go", "src/*" covers "src/a/b.go" because
// "src/*" matches the ancestor "src/a"). A pattern longer than the path never
// matches. A pattern that normalizes to the root itself ("", ".", "/")
// matches every path under the root. A pattern with a malformed glob never
// matches (fail closed), and the caller must have resolved rel through
// ResolveSafePath first, so containment in root is already guaranteed.
//
// FIX-D1a (R3-2): a target component carrying the Win32 stream separator is out
// of scope on every arm. No pattern can bound a name the OS resolves as a stream
// of some other file, so the matcher refuses instead of matching the written
// spelling. The write guard's shape arm refuses the same components by name; this
// arm is what keeps a SCOPED read or walk from claiming one.
func PathInScope(rel string, scope []string) bool {
	target := canonicalScopeTarget(normalizeScopePath(rel))
	if _, why := streamSyntaxComponent(target); why != "" {
		return false
	}
	tc := strings.Split(target, "/")
	for _, pattern := range scope {
		pc := splitScopePattern(pattern)
		if len(pc) == 0 {
			return true // root-wide pattern
		}
		if len(pc) > len(tc) {
			continue
		}
		matched := true
		for i, c := range pc {
			ok, err := path.Match(c, tc[i])
			if err != nil || !ok {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// resolvedPathInScope reports whether the symlink-resolved REAL path of target
// stays inside the unit scope (B3, FIX A2). The path-reading tools (os.Open,
// the WalkDir guard) FOLLOW symlinks, but ResolveSafePath returns the LEXICAL
// path and the scope check matches that, so a symlink planted inside a unit's
// scope that points at an out-of-scope in-root target would otherwise leak
// cross-unit content. Matching the resolved path closes that aliasing hole.
//
// Both root and target are resolved through EvalSymlinks so the two sides are
// compared in the same real-path space: a symlinked root (a temp dir on some
// hosts) would otherwise make filepath.Rel produce a bogus "../.." prefix. It
// fails closed on purpose: a resolution error, a target that is not relative to
// the root, or a resolved path outside the scope all return false and the caller
// refuses. root and target must already be absolute and root-contained (the
// caller passes ResolveSafePath output), and scope must be non-empty.
func resolvedPathInScope(root, target string, scope []string) bool {
	rel, err := resolvedRelPath(root, target)
	if err != nil {
		return false
	}
	return PathInScope(rel, scope)
}

// resolvedRelPath renders the symlink-resolved REAL path of target as a path
// relative to the symlink-resolved root: both sides are EvalSymlinks'd so the
// two are compared in the same real-path space (a symlinked root would
// otherwise make filepath.Rel produce a bogus "../.." prefix). It fails closed:
// a resolution error or a resolved path that escapes the root yields an error.
// root and target must already be absolute and root-contained.
func resolvedRelPath(root, target string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootReal, resolvedReal)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved real path escapes root: %q", rel)
	}
	return rel, nil
}

// ResolvedRelPath is the exported form of resolvedRelPath (D5): the partition
// merge's disjointness audit needs the resolved root-relative path itself (as
// the file-ownership claim key), not just the boolean scope verdict. The
// resolved claim test composes it with PathInScope, which is exactly the
// resolvedPathInScope test the per-seat read filter runs. Semantics are
// identical to the unexported original.
func ResolvedRelPath(root, target string) (string, error) {
	return resolvedRelPath(root, target)
}

// walkEntryInScope is the C1 per-entry unit-scope check for the Grep/Glob/
// ListDir directory walks. WalkDir hands each discovered entry to the walk
// callback as named under the walk root, and the path tools os.Open entries
// (following symlinks), so guardWalkFile's root-only validation is not enough
// for a scoped seat: a file symlink planted inside the unit's scope with a
// lexical in-scope name but an out-of-scope in-root target would be opened and
// the other unit's content leaked into the walk output. This check applies the
// SAME two tests the addressed path gets in runner.scopeRefusal - lexical
// PathInScope on the root-relative path AND resolvedPathInScope on the
// EvalSymlinks real path - and fails closed on any resolution error. A nil or
// empty scope returns true, leaving the walk byte-identical to the legacy
// nil-scope path (sandbox_test.go pins that behavior). rootAbs must be the
// cleaned absolute workspace root and p an absolute root-contained entry.
func walkEntryInScope(rootAbs, p string, scope []string) bool {
	if len(scope) == 0 {
		return true
	}
	rel, err := filepath.Rel(rootAbs, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if !PathInScope(rel, scope) {
		return false
	}
	return resolvedPathInScope(rootAbs, p, scope)
}

// splitScopePattern normalizes one scope entry and splits it into components.
// Backslashes become "/" (Windows-authored plans), the entry is cleaned and
// stripped of a leading "/", and an entry that denotes the root itself yields
// zero components (match-everything).
func splitScopePattern(pattern string) []string {
	s := normalizeScopePath(pattern)
	if s == "" || s == "." {
		return nil
	}
	return strings.Split(s, "/")
}

// normalizeScopePath renders a path or pattern in the canonical form the
// matcher compares: forward slashes, path.Clean'd, no leading "/".
func normalizeScopePath(s string) string {
	s = filepath.ToSlash(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = path.Clean(s)
	s = strings.TrimPrefix(s, "/")
	if s == "." {
		return ""
	}
	return s
}

// ---------------------------------------------------------------------------
// FIX-A1 (S2): Win32 canonical-form guarding.
//
// Every lexical check above assumes the path it inspects is the path Win32
// will open. It is not, in three measured ways (the probes live in
// scratch/fix-a1 and the pins in guard_canonical_test.go /
// guard_canonical_windows_test.go):
//
//   - NTFS/Win32 strips trailing dots and spaces from a component before it
//     opens the file, so write_file("sub/AGENTS.md.") CREATES sub/AGENTS.md
//     (a ReadDir after the write shows the stripped name), and the denylist,
//     which compared "AGENTS.md.", never fired. Same for "rules." and
//     "AGENTS.MD ".
//   - A DOS device name ("NUL", "sub/nul.txt") addresses a device, not a file.
//   - An 8.3 short name ("PROVID~1.JSON") can reach a governance file under a
//     string no lexical denylist match will ever see.
//
// Measured correction to finding S2's wording: a DECORATED dot-dot component
// (".. ", ".. .", "...") does not traverse on this host. Windows strips it to
// nothing, so "src/.. /.. /outside/x.txt" collapses toward "src/outside/..."
// and a write through it fails at directory creation ("The system cannot find
// the path specified") rather than escaping the root. Only the exact ".."
// traverses. The guard refuses the decorated shape by name anyway, and does NOT
// try to predict which of the two Win32 outcomes applies: a component whose
// written form is not its canonical form is refused, because a guard that
// emulates the strip is one Windows revision away from being wrong.
//
// One layer answers all three on the WRITE side, and only there: reads and
// searches keep the exact behaviour they had (the path tools' own resolved-
// path arms still run, unchanged). The section is appended rather than inlined
// because the anchors docs/code.md and internal/codesnap cite are line
// references into this file. FIX-D1a added the stream syntax, the name-class
// fold and the component-wide shape arm below, which moved the declarations
// after PathInScope. The anchors the OTHER files cite moved; current numbers
// (re-grep before trusting one):
// governanceRefusal:134, refuseProtectedWrite:177, PathInScope:301,
// ResolvedRelPath:383, canonicalTrimSet:496, canonicalWriteComponent:537,
// CanonicalPathComponentOK:628, GovernanceWriteRefusal:644,
// canonicalWriteRefusal:704, VCSMetadataRefusal:803.
// FIX-D1a additions: win32NameFold:895, Win32NameFold:904, HasWin32StreamSyntax:916,
// streamBaseOf:924, streamSyntaxComponent:950, componentShapeFailure:972,
// Win32ShortAliasShape:990. Call sites:
//
//   - refuseProtectedWrite (guard.go:177) runs canonicalWriteRefusal for both
//     write tools (write_file / replace_in_file);
//   - PathInScope (guard.go:301) refuses a stream-syntax target and
//     canonicalizes the target before matching, so the scope gate compares the
//     components the OS will use.

// canonicalTrimSet is the character set Win32 strips from the END of every path
// component before it resolves the name. NTFS keeps no trailing dot or space in
// a component: the written form and the opened form differ, and a guard that
// compares the written form is guarding a name nothing will use. Mirrored
// field-for-field in internal/codework/plan.go; TestGovernanceScopeSetMatchesGuard
// pins the two copies.
const canonicalTrimSet = ". "

// reservedDeviceNames are the DOS device names Win32 resolves in EVERY
// directory and with ANY extension: "NUL", "nul", "sub/nul.txt" and "CON.log"
// all address a device, not a file. Lowercase keys, compared after the
// canonical trailing trim and after cutting the name at its first dot.
var reservedDeviceNames = map[string]bool{
	"con":  true,
	"prn":  true,
	"aux":  true,
	"nul":  true,
	"com1": true,
	"com2": true,
	"com3": true,
	"com4": true,
	"com5": true,
	"com6": true,
	"com7": true,
	"com8": true,
	"com9": true,
	"lpt1": true,
	"lpt2": true,
	"lpt3": true,
	"lpt4": true,
	"lpt5": true,
	"lpt6": true,
	"lpt7": true,
	"lpt8": true,
	"lpt9": true,
}

// canonicalWriteComponent returns the Win32-canonical form of one path
// component: the component with the trailing dots and spaces Win32 strips.
// Callers compare THIS form, never the written form.
//
// The literal "." and ".." are returned unchanged. Two reasons, both measured:
// Win32 special-cases those two entries and never strips them, and the guard
// needs to keep seeing them as traversal so componentHygieneFailure can name
// them (a naive TrimRight turns ".." into "", which reads as a vanished
// component). Every decorated variant (".. ", ".. .", "...") does strip to an
// empty string, which is exactly what Windows does with those components.
func canonicalWriteComponent(comp string) string {
	if comp == "." || comp == ".." {
		return comp
	}
	return strings.TrimRight(comp, canonicalTrimSet)
}

// deviceNameOf returns the DOS device base of a component: the canonically
// trimmed component cut at its first dot, folded with win32NameFold. "CON.txt",
// "con" and "NUL." all reduce to a device name; "console.go" does not.
func deviceNameOf(comp string) string {
	base := canonicalWriteComponent(comp)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return win32NameFold(base)
}

// componentHygieneFailure names why one path component is not a safe write
// target on a Win32 host, or "" when it is. The three reasons map onto the
// three ways a written component can name something other than itself:
//
//   - it ends in a dot or a space, which Win32 strips, so the write lands under
//     a name the guard never compared ("AGENTS.md.", "AGENTS.md ", "rules.");
//   - it canonicalizes to nothing ("...", "   "), which Win32 cannot open at all;
//   - it is a reserved DOS device name (with or without an extension), which
//     addresses a device instead of a file.
//
// An exact "." or ".." component is refused too: a root-relative path that
// still carries a traversal segment is not a canonical guarded location. The
// literal forms are already removed by filepath.Clean, so in practice this arm
// sees ".." only when the caller passed an already-relative path straight
// through, and the decorated variants (".. ", ".. .") land under the empty-name
// arm above.
//
// This function is the CANONICAL-FORM half of the per-component test only; the
// stream-syntax (R3-2) and 8.3-shape (R3-3) arms live in componentShapeFailure,
// which is what every caller of the guard actually runs.
func componentHygieneFailure(comp string) string {
	if comp == "" {
		// A leading "/" or a doubled separator yields an empty segment; that is
		// a separator artefact, not a component.
		return ""
	}
	canon := canonicalWriteComponent(comp)
	switch {
	case canon == "":
		return "canonicalizes to an empty name (Win32 strips every trailing dot and space)"
	case canon == "." || canon == "..":
		return "is a path traversal component"
	case canon != comp:
		return "ends with a dot or a space, which Win32 strips: the write would land under a different name than the one checked"
	}
	if reservedDeviceNames[deviceNameOf(comp)] {
		return "is a reserved DOS device name: the write would address a device instead of a file"
	}
	return ""
}

// nonCanonicalComponent returns the first component of the root-relative slash
// path rel that fails componentShapeFailure, plus that reason. It reports
// ("", "") when every component is clear. FIX-D1a: the per-component test is
// componentShapeFailure (stream syntax, canonical form, 8.3 shape), so every
// caller of this function - canonicalWriteRefusal, CanonicalPathComponentOK and
// the exported predicates - inherits all three arms from one scan.
func nonCanonicalComponent(rel string) (string, string) {
	for _, comp := range strings.Split(filepath.ToSlash(rel), "/") {
		if why := componentShapeFailure(comp); why != "" {
			return comp, why
		}
	}
	return "", ""
}

// CanonicalPathComponentOK reports whether every component of the
// root-relative slash path relPath is a name the OS will open as written. It is
// the exported form of nonCanonicalComponent's test, for guards outside this
// package (the rollback/restore writer is the first consumer): a manifest row or
// a restore target that fails here must be refused, because the lexical
// containment and denylist checks it already passed were made against a name
// Windows will not use.
//
// It returns ("", true) when the path is clear, and (badComponent, false) when a
// component is not usable: it ends with a dot or space, canonicalizes to
// nothing, is a trailing "." / ".." traversal segment, is a reserved DOS device
// name, carries the Win32 stream separator (R3-2, Windows only), or has the 8.3
// alias shape (R3-3, Windows only). The canonical-form and device-name arms
// answer identically on every host, so a name that means two different files on
// the host this guard exists for is refused everywhere; the two 8.3/stream arms
// follow the host, because a POSIX file may legally be called "notes:2026.md"
// and a POSIX host mints no aliases.
func CanonicalPathComponentOK(relPath string) (string, bool) {
	comp, why := nonCanonicalComponent(relPath)
	if why != "" {
		return comp, false
	}
	return "", true
}

// GovernanceWriteRefusal is the exported governance/credential predicate: it
// reports whether the root-relative slash path relPath addresses a governed
// target, and names the matching component. Matching is component-exact over the
// CANONICAL form of each component (trailing dots and spaces removed) and
// case-insensitive, so "AGENTS.MD ", "agents.md" and "Agents.Md." are the same
// target here. Exported for the guards outside this package that gate a write
// path they resolved themselves (the rollback/restore writer); internal callers
// use the unexported governanceRefusal, which this wraps.
func GovernanceWriteRefusal(relPath string) (string, bool) {
	return governanceRefusal(filepath.ToSlash(relPath))
}

// win32ExpandWriteTargetFn is the platform 8.3 long-name expansion hook
// (guard_win32_windows.go / guard_win32_other.go). It is a variable so the
// Windows pin can drive the arm with a synthetic expansion on a volume that
// mints no aliases: the workspace volume (D:) generates none (no short names in
// `dir /x`), while the temp volume (C:) does, which is why the live alias PoC
// runs from a temp root and the synthetic pin covers a root on D:.
// Production code never assigns it.
var win32ExpandWriteTargetFn = win32ExpandWriteTarget

// shortAliasWriteReason is the refusal reason of the 8.3 shape sub-rule. It is
// phrased for the model that produced the path: it names what is wrong with the
// WRITTEN component and why the guard cannot resolve it instead.
const shortAliasWriteReason = "has the Win32 8.3 short-name shape (NAME~1.EXT), which can address a file whose lexical name the guard never sees (8.3 alias expansion is the only reliable resolution, and the alias cannot be resolved for a name that does not exist yet)"

// canonicalWriteRefusal is the front door of the write guard: it refuses a
// write target whose Win32-resolved name differs from the name every lexical
// check above compared. It runs for both write tools, from refuseProtectedWrite
// (guard.go:193), after the VCS-metadata arm and before the resolved-dir arm.
//
// Order and why:
//
//  1. the per-component shape scan — componentShapeFailure, which is the
//     canonical-form arm (A1: the written form must BE the canonical form, so
//     "AGENTS.md.", "AGENTS.md ", "rules./team-rules.md", ".. " and the DOS
//     device names are refused), the R3-2 stream arm (a ':' anywhere in a
//     component addresses a stream, never a file of that name, and
//     GetLongPathNameW answers 0 for such a path), and the R3-3 8.3 shape arm on
//     EVERY component. The shape arm is the APPROXIMATE sub-rule: a name that
//     does not exist yet cannot be expanded through GetLongPathNameW (the API is
//     an existence lookup), so the classic generated-alias shape NAME~N.EXT is
//     refused outright rather than resolved. It is a shape test, not a
//     resolution: a hand-made "notes~1.txt" is refused too. That over-refusal is
//     deliberate and bounded to names a plan author controls; the alternative is
//     leaving the vector open on every volume whose 8.3 generation is enabled.
//     Every component is tested because MkdirAll creates an alias-named
//     intermediate directory THROUGH the alias, so a leaf-only test is blind to
//     "PROVID~1/newdir/sub/x.md".
//  2. governance on the canonical lexical components — governanceRefusal already
//     compares canonical forms (folded, stream base), so this catches
//     "AGENTS.MD " / "agents.md." / "AGENTS.md:evil" even when the shape arm's
//     reason is not the one reported.
//  3. 8.3 long-name expansion of the longest EXISTING ancestor — the exact arm:
//     GetLongPathNameW on the target (an existing file, the replace_in_file
//     case), else on the nearest ancestor that exists, with the remainder
//     rejoined verbatim (the not-yet-existing write case, at any depth). Any
//     expansion that lands on a governed or VCS-protected component is refused.
//     On a volume with 8.3 generation disabled the expansion returns the long
//     name it was given, so this arm is inert there and the shape rule above
//     carries the vector; the Windows pin drives it synthetically through
//     win32ExpandWriteTargetFn so the arm is exercised on such a volume too.
//
// APPROXIMATION, stated plainly: sub-rules 1 and 2 are exact for the forms they
// name; the 8.3 shape arm is a shape heuristic; sub-rule 3 is exact on a volume
// with 8.3 generation enabled and inert on one without it. There is no arm that
// enumerates a directory to find an alias for a name that does not exist yet
// (that would cost a full directory read per write and still lose the race).
func canonicalWriteRefusal(rootAbs, safePath, relSlash string) error {
	// Step 1: one scan per component (stream syntax, canonical form, 8.3 shape
	// on EVERY component). The leaf-only shape test FIX-A1 shipped lived here as
	// a second block; R3-3 folded it into componentShapeFailure so an alias in an
	// intermediate position is refused too.
	if comp, why := nonCanonicalComponent(relSlash); why != "" {
		return fmt.Errorf("access denied: %q path component %q %s; write_file/replace_in_file refuse it", relSlash, comp, why)
	}

	if comp, refuse := governanceRefusal(relSlash); refuse {
		return governanceWriteRefusal(relSlash, comp)
	}

	expanded, ok := win32ExpandWriteTargetFn(safePath)
	if !ok {
		// No resolvable prefix (a write into a directory chain that does not
		// exist yet): the shape scan above is the only 8.3 arm that can apply.
		// This branch must not be a blanket "allow" either - GetLongPathNameW
		// answers 0 for a path it cannot resolve at all, and a colon path is
		// exactly that (R3-2) - so the stream arm is re-checked before the early
		// return, independently of the step-1 scan.
		if comp, why := streamSyntaxComponent(relSlash); why != "" {
			return fmt.Errorf("access denied: %q path component %q %s; write_file/replace_in_file refuse it", relSlash, comp, why)
		}
		return nil
	}
	expandedRel, err := filepath.Rel(rootAbs, expanded)
	if err != nil {
		return fmt.Errorf("access denied: %q resolves, through its Win32 8.3 long name %q, to a path that cannot be verified against the workspace root; write_file/replace_in_file refuse it", relSlash, expanded)
	}
	expandedSlash := filepath.ToSlash(expandedRel)
	if expandedSlash == relSlash {
		return nil
	}
	if expandedSlash == ".." || strings.HasPrefix(expandedSlash, "../") {
		return fmt.Errorf("access denied: %q resolves, through its Win32 8.3 long name, outside the workspace root (%q); write_file/replace_in_file refuse it", relSlash, expandedSlash)
	}
	if comp, refuse := governanceRefusal(expandedSlash); refuse {
		return fmt.Errorf("access denied: %q resolves, through its Win32 8.3 long name, to %q, which is a governance/credential file (%s) that configures the agent system itself; write_file/replace_in_file refuse it", relSlash, expandedSlash, comp)
	}
	for _, comp := range strings.Split(expandedSlash, "/") {
		if protectedWriteNames[win32NameFold(streamBaseOf(canonicalWriteComponent(comp)))] {
			return fmt.Errorf("access denied: %q resolves, through its Win32 8.3 long name, to %q, which is protected VCS metadata (%s); write_file/replace_in_file refuse it", relSlash, expandedSlash, comp)
		}
	}
	return nil
}

// canonicalScopeTarget canonicalizes the components of a scope-matching TARGET
// (never a pattern) the way Win32 resolves them, then re-cleans the result, so
// the scope gate sees the path the write will actually open. PathInScope calls
// it; scope PATTERNS are left byte-identical so an authored scope keeps matching
// the names its author wrote.
//
// "src/.. /.. /outside/x.txt" becomes "src/outside/x.txt" here: the two ".. "
// components are what Windows strips to nothing, so the target keeps matching
// the pattern the OS's path would match (the pre-canonical matcher happened to
// agree on that shape for the wrong reason: path.Match's "*" matched the
// literal ".. " component, so the verdict was a coincidence, not a
// canonicalization). An exact ".." is preserved and still collapses through
// path.Clean, so a real traversal keeps reading as out of scope. A target with
// no decorated components is returned unchanged, so this is a no-op for every
// name that exists on disk.
func canonicalScopeTarget(target string) string {
	if target == "" {
		return ""
	}
	parts := strings.Split(target, "/")
	kept := make([]string, 0, len(parts))
	for _, comp := range parts {
		if comp = canonicalWriteComponent(comp); comp != "" {
			kept = append(kept, comp)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	cleaned := path.Clean(strings.Join(kept, "/"))
	if cleaned == "." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

// VCSMetadataRefusal is the exported VCS-metadata predicate: it reports whether
// any component of the root-relative slash path relPath is VCS metadata, and
// names the matching component. It matches the arm refuseProtectedWrite
// (guard.go:188) runs for the write tools on the WRITTEN path, over the FOLDED
// STREAM BASE of the canonical component form, so ".GIT", ".git.", ".git" and
// ".git:evil" are one target.
//
// The guard also applies this predicate to the RESOLVED directory chain
// (refuseProtectedWrite -> resolvedDirChain), which is what closes the in-root
// link alias; this exported form is what external guards apply to a path they
// resolved themselves.
//
// Exported for the guards outside this package that gate a path they resolved
// themselves (the snapshot/rollback ledger writer is the first consumer: a
// restored .git/config is code execution on the operator's next git command).
func VCSMetadataRefusal(relPath string) (string, bool) {
	for _, comp := range strings.Split(filepath.ToSlash(relPath), "/") {
		if comp == "" {
			continue
		}
		if protectedWriteNames[win32NameFold(streamBaseOf(canonicalWriteComponent(comp)))] {
			return comp, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// FIX-D1a (R3-1 / R3-2 / R3-3): one name-class fold, the Win32 stream syntax,
// and the 8.3 shape rule on EVERY component.
//
// The A1 layer above compares the WRITTEN form of a component against the form
// the guard expects. Three gaps remained, all of them about the NAME itself
// rather than its trailing characters. Probes: scratch/fix-d1a (before/after
// captured in coder-report-fix-d1a.md); pins: guard_namefold_test.go,
// guard_stream_test.go, guard_canonical_test.go,
// guard_canonical_windows_test.go.
//
//   - R3-1 case folding. Every name comparison above lowercases with
//     strings.ToLower, but the fold the FILESYSTEM applies is NTFS's $UpCase
//     table, which is not Unicode lowercase. Measured on this host: the table
//     maps U+212A KELVIN SIGN to ASCII "K" in the $UpCase payload, but the two
//     spellings are DISTINCT files on both probed volumes ("K.txt" and "k.txt"
//     coexist), so no collision was measured for it - the earlier "write K.txt
//     and k.txt is one file" sentence in this comment was wrong and is retired
//     (P-9; measurement: decisions.md, internal/codesnap/snapshot.go:753, the
//     `dirKey` fold measurement taken by `probeCaseFolding`). The pair the
//     finding names, U+017F "ſ" -> "s", did NOT collide either (see the
//     report's collision matrix), which downgrades R3-1 to the spoof class on
//     this host. It still gets the fix, because $UpCase is a PER-VOLUME table
//     written when the volume is formatted: a volume formatted elsewhere can
//     fold a different set. One fold function now answers "are these the same
//     name" for every comparison, so no arm can fold differently from another;
//     where the fold is wider than the volume it over-refuses, which is the
//     fail-closed direction (U+0131, U+017F).
//   - R3-2 stream syntax. On NTFS "name:stream" and "name::$DATA" address a
//     STREAM of the file "name", and CreateFileW accepts both (measured: a
//     direct write to "x.md::$DATA" replaces x.md's content). Both shapes
//     passed every predicate above, and GetLongPathNameW answers 0 for a colon
//     path, so the 8.3 expansion arm's "no resolution -> allow" branch
//     swallowed them.
//   - R3-3 intermediate 8.3 aliases. The shape arm tested the FINAL component
//     only and the expansion arm expanded the immediate parent only, so
//     "PROVID~1/newdir/sub/x.md" - newdir and sub created by MkdirAll THROUGH
//     the alias - landed inside ProvidersRegistryBackup with no arm firing.
//
// The three arms live in one per-component function, componentShapeFailure, so
// a write target and a scope entry are judged by the same rules; the mirror in
// internal/codework/plan.go carries the same three arms for plan-time scopes.
// Call sites:
//
//   - nonCanonicalComponent (:602) runs componentShapeFailure per component, so
//     canonicalWriteRefusal, CanonicalPathComponentOK and every exported
//     predicate below inherit all three arms;
//   - PathInScope (:301) refuses a stream-syntax target outright: no scope
//     pattern can bound a name the OS resolves as a stream;
//   - governanceRefusal (:134) and the VCS loop in refuseProtectedWrite (:188)
//     compare the FOLDED STREAM BASE of each component;
//   - deviceNameOf (:547) folds the device base;
//   - win32ExpandWriteTarget (guard_win32_windows.go) walks up to the longest
//     existing ancestor instead of stopping at the immediate parent.

// win32NameFold returns the comparison key for one path component name: the
// NTFS $UpCase fold, approximated with strings.ToUpper and then lowercased so
// the denylist maps keep their lowercase keys. It is the ONLY fold the write
// guard uses for name-class comparisons, so governance, VCS metadata, DOS
// device names and (through the export below) the snapshot ledger's secret-class
// names cannot disagree about which spellings are one name.
//
// Why uppercase: strings.ToLower leaves U+017F (LATIN SMALL LETTER LONG S, "ſ")
// alone while strings.ToUpper maps it to "S", and $UpCase is an uppercase table,
// so the uppercase fold is the direction that can match it.
//
// LIMITS, stated plainly - this is a fold, not the NTFS table:
//
//   - it folds MORE than $UpCase in a few places (U+0131 dotless "ı" folds to
//     "I" here, while Windows keeps it distinct). Those extra merges fail
//     CLOSED: a name that merely LOOKS governed is refused, never allowed.
//   - it folds FEWER in others: Go's simple case mapping does not expand ("ß"
//     stays "ß", never "SS"), and characters $UpCase maps specially outside the
//     simple-uppercase rule are left alone. Such a spelling keeps exactly the
//     verdict the plain compare gave.
//   - it does not fold superscript-digit look-alikes ("com¹"), the recorded
//     R3-11 spoof residue: measured, NTFS gives them no collision with the ASCII
//     forms, so a fold has nothing to close there.
//   - it folds the whole component, not just the name before the extension,
//     because $UpCase is applied per UTF-16 code unit to the whole name.
func win32NameFold(name string) string {
	return strings.ToLower(strings.ToUpper(name))
}

// Win32NameFold is the exported fold (FIX-D1a), for guards outside this package
// that compare name classes the way this one does: the snapshot/rollback ledger
// (D1b) folds both sides of its scope-claim and archive-destination comparisons
// with it, and its secret-class name test compares the folded name against
// folded suffixes (".PEM" and ".pem" fold alike). Keys stay lowercase.
func Win32NameFold(name string) string {
	return win32NameFold(name)
}

// HasWin32StreamSyntax reports whether one path component carries the Win32
// alternate-data-stream separator ':'. On NTFS "name:stream" and "name::$DATA"
// address a stream of the file "name" (or of a device), never a file of their
// own, so no lexical containment rule, no denylist entry and no scope pattern
// can bound them by comparing the written string.
//
// The predicate is platform-neutral; the REFUSAL is not (win32StreamSyntaxRule
// below), because a colon is an ordinary filename character on POSIX.
func HasWin32StreamSyntax(component string) bool {
	return strings.IndexByte(component, ':') >= 0
}

// streamBaseOf returns the part of a component that names the FILE: everything
// before the first ':'. "AGENTS.md:evil" and "AGENTS.md::$DATA" both name the
// file AGENTS.md, so governance and VCS comparisons run on this base; the
// component itself is refused outright by the shape arm before any write.
func streamBaseOf(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[:i]
	}
	return name
}

// streamSyntaxWriteReason is the refusal reason of the R3-2 arm. Phrased for the
// model that produced the path: it names the character, what the OS does with
// it, and why the write tools refuse instead of resolving it.
const streamSyntaxWriteReason = "contains ':', the Win32 alternate-data-stream separator, so the name addresses a stream of a file (or of a device) instead of a file of that name"

// streamSyntaxFailure returns the R3-2 refusal reason for one path component, or
// "" when the component is clear or the rule is off on this host
// (win32StreamSyntaxRule, guard_win32_windows.go / guard_win32_other.go).
func streamSyntaxFailure(comp string) string {
	if !win32StreamSyntaxRule || comp == "" || !HasWin32StreamSyntax(comp) {
		return ""
	}
	return streamSyntaxWriteReason
}

// streamSyntaxComponent returns the first component of the root-relative slash
// path rel that fails the R3-2 arm, plus that reason, or ("", ""). It exists for
// call sites that are not already walking the components (the 8.3 expansion
// branch of canonicalWriteRefusal, the scope matcher).
func streamSyntaxComponent(rel string) (string, string) {
	for _, comp := range strings.Split(filepath.ToSlash(rel), "/") {
		if why := streamSyntaxFailure(comp); why != "" {
			return comp, why
		}
	}
	return "", ""
}

// componentShapeFailure returns everything wrong with one path component as a
// write target, in the order the guard tests it:
//
//  1. R3-2 stream syntax - the name addresses a stream, not a file;
//  2. the Win32 canonical form (componentHygieneFailure) - dots, spaces, device
//     names, components that strip to nothing;
//  3. R3-3 the 8.3 alias shape, on THIS component. A1 tested the final
//     component only; an alias in an intermediate position is just as good a
//     name, because MkdirAll creates the directories behind it through the
//     alias.
//
// The shape arms are per component and cheap (a byte scan), and the whole scan
// runs on the root-relative path, so depth costs nothing but the split.
func componentShapeFailure(comp string) string {
	if why := streamSyntaxFailure(comp); why != "" {
		return why
	}
	if why := componentHygieneFailure(comp); why != "" {
		return why
	}
	if win32ShortAliasShape(comp) {
		return shortAliasWriteReason
	}
	return ""
}

// Win32ShortAliasShape is the exported 8.3 shape test (FIX-D1a), for guards
// outside this package that gate names they resolved themselves: the
// snapshot/rollback row policy (D1b) refuses a manifest row whose ANY component
// has the shape, which is the same per-component test this guard runs. False on
// a host that has no 8.3 names (guard_win32_other.go).
func Win32ShortAliasShape(component string) bool {
	return win32ShortAliasShape(component)
}

// ResolvedDirChainRefusal is the exported form of the RESOLVED DIRECTORY CHAIN
// arm refuseProtectedWrite runs for both write tools (guard.go:196-205): the
// chain is the real directory path a write to target traverses, produced by
// resolvedDirChainRelPath (runner.go) - the FINAL component is deliberately
// excluded, because the write tools replace a file symlink instead of writing
// through it, so only the directories are an escalation surface. That exclusion
// is unconditional now (FIX-F1, P-8): the walk starts at the target's parent, so
// an existing final symlink is appended verbatim like any other final name
// instead of being resolved, which is what this contract always said and what
// the over-refusing rollback case proved it did not do.
//
// It exists for the guards outside this package that restore or move a file they
// resolved themselves. internal/codesnap is the first consumer: a rollback row is
// judged lexically by the manifest row policy (CanonicalPathComponentOK,
// GovernanceWriteRefusal, VCSMetadataRefusal on the written path), and a row such
// as "link/registry-keys.v2.json" with "link" resolving to ".agy-pro" passes every
// one of those while the restore would land inside the governance directory
// (finding N6). The resolution logic is NOT duplicated there: this function and
// the write guard share resolvedDirChain, one walk judging BOTH classes (R5-1:
// the write arm judged only the governance half).
//
// Returns the resolved directory (root-relative slash form), the offending
// component when refused, and an error when the chain cannot be resolved at all
// (no existing ancestor, a component below the target that exists but is not a
// directory, a non-"not exist" Lstat failure, or a chain that escapes the root)
// so the caller can fail closed instead of guessing. A target whose directory
// chain is the root itself ("a.go", "src/../a.go") is clear.
func ResolvedDirChainRefusal(root, target string) (resolvedDir string, comp string, refused bool, err error) {
	rootAbs := "."
	if strings.TrimSpace(root) != "" {
		rootAbs = root
	}
	rootAbs, aerr := filepath.Abs(filepath.Clean(rootAbs))
	if aerr != nil {
		return "", "", false, fmt.Errorf("failed to resolve root directory: %w", aerr)
	}
	absTarget := filepath.Clean(target)
	if !filepath.IsAbs(absTarget) {
		// A root-relative target is what the rollback has (a manifest row); the
		// write guard always passes an absolute ResolveSafePath output.
		absTarget = filepath.Join(rootAbs, absTarget)
	}
	verdict, verr := resolvedDirChain(rootAbs, absTarget)
	if verr != nil {
		return "", "", false, verr
	}
	return verdict.dir, verdict.comp, verdict.class != resolvedChainClear, nil
}
