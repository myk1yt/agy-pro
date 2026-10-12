package agenttools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxReadFileBytes   = 512 * 1024 // 512 KB
	maxCommandOutput   = 64 * 1024  // 64 KB
	defaultCommandWait = 30 * time.Second
	// maxReplaceFileBytes caps the file size ReplaceInFile accepts, aligned
	// with the read cap: a unique anchor can only be chosen inside content
	// the model can actually see.
	maxReplaceFileBytes = maxReadFileBytes
)

// maxToolTimeoutSeconds is the per-tool budget applied at the tool-dispatch
// layer (R1-5): a model cannot request a longer run_command wait through the
// runner. RunCommand's own 120s hard cap stays as the absolute ceiling for
// direct callers.
const maxToolTimeoutSeconds = 30

// clampToolTimeout caps a model-supplied timeout_seconds at the per-tool
// budget (R1-5). Non-positive values pass through so RunCommand keeps its
// default wait.
func clampToolTimeout(seconds int) int {
	if seconds > maxToolTimeoutSeconds {
		return maxToolTimeoutSeconds
	}
	return seconds
}

// ReadFile reads a file within root, optionally slicing by line numbers (1-indexed).
func ReadFile(root, path string, startLine, endLine int) (string, error) {
	safePath, err := ResolveSafePath(root, path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("file not found: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("target is a directory, not a file: %s", path)
	}

	f, err := os.Open(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(io.LimitReader(f, maxReadFileBytes))
	lineNum := 1
	for scanner.Scan() {
		if (startLine <= 0 || lineNum >= startLine) && (endLine <= 0 || lineNum <= endLine) {
			lines = append(lines, fmt.Sprintf("%4d | %s", lineNum, scanner.Text()))
		}
		lineNum++
		if endLine > 0 && lineNum > endLine {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read error: %w", err)
	}

	if len(lines) == 0 {
		return "(empty file or line range outside file bounds)", nil
	}
	return strings.Join(lines, "\n"), nil
}

// ListDir lists directory contents within root up to maxDepth.
func ListDir(root, path string, recursive bool, maxDepth int) (string, error) {
	if path == "" {
		path = "."
	}
	safePath, err := ResolveSafePath(root, path)
	if err != nil {
		return "", err
	}

	if maxDepth <= 0 {
		maxDepth = 2
	}
	if maxDepth > 5 {
		maxDepth = 5
	}

	var results []string
	count := 0
	const maxEntries = 250

	startDepth := strings.Count(filepath.Clean(safePath), string(filepath.Separator))

	err = filepath.WalkDir(safePath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// WalkDir reports a failed root with a nil DirEntry; surface that
			// error to the caller instead of swallowing it.
			if p == safePath {
				return err
			}
			return nil
		}
		if d == nil {
			return nil
		}
		if count >= maxEntries {
			return fs.SkipAll
		}
		if p == safePath {
			return nil
		}

		currentDepth := strings.Count(p, string(filepath.Separator)) - startDepth
		if !recursive && currentDepth > 1 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if recursive && currentDepth > maxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Skip common noise directories
		name := d.Name()
		if d.IsDir() && (name == ".git" || name == "node_modules" || name == ".gemini") {
			return fs.SkipDir
		}

		rel, _ := filepath.Rel(safePath, p)
		// Symlink/reparse entries are listed but marked: a link can point
		// outside the workspace and the reader should see that at a glance
		// (S-4 optional hardening; ListDir never opens file content).
		if t := d.Type(); t&fs.ModeSymlink != 0 || t&fs.ModeIrregular != 0 {
			results = append(results, fmt.Sprintf("[LINK] %s (symlink/junction)", rel))
			count++
			return nil
		}
		if d.IsDir() {
			results = append(results, fmt.Sprintf("[DIR]  %s/", rel))
		} else {
			info, _ := d.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			results = append(results, fmt.Sprintf("[FILE] %s (%d bytes)", rel, size))
		}
		count++
		return nil
	})

	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("list_dir: path does not exist: %s", path)
		}
		return "", fmt.Errorf("list directory error: %w", err)
	}

	if len(results) == 0 {
		return "(directory is empty)", nil
	}
	if count >= maxEntries {
		results = append(results, fmt.Sprintf("... (truncated at %d entries)", maxEntries))
	}
	return strings.Join(results, "\n"), nil
}

// guardWalkFile re-validates one discovered walk entry against the workspace
// root before its content is opened (S-4/F1). WalkDir yields paths as named
// under the walk root; a file symlink or junction there can resolve outside
// the root, and opening it would leak outside content into the tool output.
// ok is false for entries that must be skipped.
func guardWalkFile(root, p string) (safePath string, ok bool) {
	safe, err := ResolveSafePath(root, p)
	if err != nil {
		return "", false
	}
	return safe, true
}

// Grep searches for regex pattern within workspace files.
func Grep(root, pattern, path string, caseInsensitive bool, maxMatches int) (string, error) {
	if path == "" {
		path = "."
	}
	safePath, err := ResolveSafePath(root, path)
	if err != nil {
		return "", err
	}

	if maxMatches <= 0 {
		maxMatches = 100
	}

	regexPattern := pattern
	if caseInsensitive {
		regexPattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return "", fmt.Errorf("invalid regular expression: %w", err)
	}

	var matches []string
	matchCount := 0
	skippedOutside := 0

	err = filepath.WalkDir(safePath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// WalkDir reports a failed root with a nil DirEntry; surface that
			// error to the caller instead of dereferencing d below.
			if p == safePath {
				return err
			}
			return nil
		}
		if d == nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".gemini" {
				return fs.SkipDir
			}
			return nil
		}
		if matchCount >= maxMatches {
			return fs.SkipAll
		}

		// Skip binary files by extension
		ext := strings.ToLower(filepath.Ext(p))
		switch ext {
		case ".exe", ".dll", ".so", ".dylib", ".png", ".jpg", ".jpeg", ".gif", ".bin", ".zip", ".tar", ".gz":
			return nil
		}

		// Re-validate every discovered entry against the workspace root
		// before opening it (S-4/F1): WalkDir hands us the path as named
		// under the walk root, but a file symlink or junction there can
		// resolve outside the root and os.Open would follow it. ResolveSafePath
		// refuses those; refused entries are skipped and counted so the tool
		// output reports the skip instead of leaving a silent hole.
		safeEntry, ok := guardWalkFile(root, p)
		if !ok {
			skippedOutside++
			return nil
		}

		f, err := os.Open(safeEntry)
		if err != nil {
			return nil
		}
		defer f.Close()

		rel, _ := filepath.Rel(root, p)
		scanner := bufio.NewScanner(io.LimitReader(f, maxReadFileBytes))
		lineNum := 1
		for scanner.Scan() {
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", rel, lineNum, line))
				matchCount++
				if matchCount >= maxMatches {
					break
				}
			}
			lineNum++
		}
		return nil
	})

	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("grep: search path does not exist: %s", path)
		}
		return "", fmt.Errorf("grep walk error: %w", err)
	}

	// One meta line for anything skipped as outside the workspace root, so a
	// missing match is never a debugging mystery (S-4/F1).
	if skippedOutside > 0 {
		matches = append(matches, fmt.Sprintf("... (skipped %d entries that resolve outside the workspace root)", skippedOutside))
	}
	if len(matches) == 0 {
		return "(no matches found)", nil
	}
	if matchCount >= maxMatches {
		matches = append(matches, fmt.Sprintf("... (truncated at %d matches)", maxMatches))
	}
	return strings.Join(matches, "\n"), nil
}

// Glob finds files matching wildcard pattern under path.
func Glob(root, pattern, path string) (string, error) {
	if path == "" {
		path = "."
	}
	safePath, err := ResolveSafePath(root, path)
	if err != nil {
		return "", err
	}

	var matches []string
	count := 0
	const maxMatches = 200

	err = filepath.WalkDir(safePath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// WalkDir reports a failed root with a nil DirEntry; surface that
			// error to the caller instead of dereferencing d below.
			if p == safePath {
				return err
			}
			return nil
		}
		if d == nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if count >= maxMatches {
			return fs.SkipAll
		}

		matched, _ := filepath.Match(pattern, d.Name())
		if !matched {
			// Check full relative path pattern
			rel, _ := filepath.Rel(safePath, p)
			matched, _ = filepath.Match(pattern, filepath.ToSlash(rel))
		}

		if matched {
			relFromRoot, _ := filepath.Rel(root, p)
			matches = append(matches, filepath.ToSlash(relFromRoot))
			count++
		}
		return nil
	})

	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("glob: search path does not exist: %s", path)
		}
		return "", fmt.Errorf("glob walk error: %w", err)
	}

	if len(matches) == 0 {
		return "(no files matched pattern)", nil
	}
	return strings.Join(matches, "\n"), nil
}

// WriteFile writes content atomically to path within root.
func WriteFile(root, path, content string, overwrite bool) (string, error) {
	safePath, err := ResolveSafePath(root, path)
	if err != nil {
		return "", err
	}
	if err := refuseProtectedWrite(root, safePath); err != nil {
		return "", err
	}

	if !overwrite {
		if _, err := os.Stat(safePath); err == nil {
			return "", fmt.Errorf("file already exists: %s (set overwrite=true to overwrite)", path)
		}
	}

	if err := atomicWriteFile(safePath, []byte(content)); err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), nil
}

// ReplaceInFile performs a targeted exact-string replacement inside an
// existing file (native replace_file_content parity, R1-9b). Without
// replaceAll the old_string must match exactly once: zero matches and
// multiple matches both fail and leave the file untouched, so the model can
// widen its anchor instead of corrupting an unintended spot. The write is
// atomic (temp+rename) behind the same ResolveSafePath guard as write_file.
func ReplaceInFile(root, path, oldString, newString string, replaceAll bool) (string, error) {
	if oldString == "" {
		return "", fmt.Errorf("old_string must not be empty")
	}

	safePath, err := ResolveSafePath(root, path)
	if err != nil {
		return "", err
	}
	if err := refuseProtectedWrite(root, safePath); err != nil {
		return "", err
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("file not found: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("target is a directory, not a file: %s", path)
	}
	if info.Size() > maxReplaceFileBytes {
		return "", fmt.Errorf("file too large for replace_in_file: %d bytes (limit %d); rewrite it with write_file", info.Size(), maxReplaceFileBytes)
	}

	raw, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	content := string(raw)

	occurrences := strings.Count(content, oldString)
	if occurrences == 0 {
		return "", fmt.Errorf("old_string not found in %s (no replacement made)", path)
	}
	if occurrences > 1 && !replaceAll {
		return "", fmt.Errorf("old_string matches %d times in %s; make it unique or pass replace_all=true (no replacement made)", occurrences, path)
	}

	updated := content
	replaced := 1
	if replaceAll {
		updated = strings.ReplaceAll(content, oldString, newString)
		replaced = occurrences
	} else {
		updated = strings.Replace(content, oldString, newString, 1)
	}

	if err := atomicWriteFile(safePath, []byte(updated)); err != nil {
		return "", err
	}

	return fmt.Sprintf("Replaced %d occurrence(s) in %s (%d -> %d bytes)", replaced, path, len(raw), len(updated)), nil
}

// atomicWriteFile writes data to a ResolveSafePath-validated destination via
// a temp file + rename in the same directory, so a crashed write never leaves
// a torn file behind.
func atomicWriteFile(safePath string, data []byte) error {
	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-write-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}

	if err := os.Rename(tmpName, safePath); err != nil {
		cleanup()
		return fmt.Errorf("failed to replace destination: %w", err)
	}
	return nil
}

// RunCommand executes a command within root directory. The child environment
// is a minimal allow-list (see sanitizedCommandEnv): model-driven commands
// must never see provider API keys or the gateway session key (S-4/F4, root
// fix - the tool dispatch inherits this through the same function).
func RunCommand(root, command string, args []string, cwd string, timeoutSeconds int) (string, error) {
	if cwd == "" {
		cwd = "."
	}
	safeCwd, err := ResolveSafePath(root, cwd)
	if err != nil {
		return "", err
	}

	timeout := defaultCommandWait
	if timeoutSeconds > 0 {
		if timeoutSeconds > 120 {
			timeoutSeconds = 120
		}
		timeout = time.Duration(timeoutSeconds) * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = safeCwd
	cmd.Env = sanitizedCommandEnv()

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	err = cmd.Run()
	output := combined.String()
	if len(output) > maxCommandOutput {
		output = output[:maxCommandOutput] + "\n... (output truncated)"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output, fmt.Errorf("command timed out after %v", timeout)
		}
		return output, fmt.Errorf("command exited with error: %w", err)
	}

	if strings.TrimSpace(output) == "" {
		return "(command completed with no output)", nil
	}
	return output, nil
}

// commandEnvAllowList is the set of variable names an invoked command may
// see, compared case-insensitively. Everything else is dropped: the list is
// default-deny, so credential variables (*_API_KEY, AGY_LLM_GATEWAY_*, ...)
// can never reach a command subprocess (S-4/F4).
var commandEnvAllowList = map[string]bool{
	// process/runtime essentials
	"path": true, "pathext": true, "comspec": true,
	"systemroot": true, "systemdrive": true, "windir": true,
	"temp": true, "tmp": true, "tmpdir": true,
	"home": true, "userprofile": true, "homedrive": true, "homepath": true,
	"appdata": true, "localappdata": true,
	"username": true, "user": true, "logname": true, "userdomain": true, "userdnsdomain": true,
	"allusersprofile": true, "public": true, "programdata": true,
	"programfiles": true, "programfiles(x86)": true, "programw6432": true,
	"commonprogramfiles": true, "commonprogramfiles(x86)": true,
	"number_of_processors": true, "os": true,
	"processor_architecture": true, "processor_identifier": true,
	"processor_level": true, "processor_revision": true,
	// locale/shell basics so ordinary commands behave
	"lang": true, "lc_all": true, "lc_ctype": true, "term": true, "shell": true,
	"pwd": true, "oldpwd": true,
}

// sanitizedCommandEnv returns the parent environment filtered down to
// commandEnvAllowList. Only allow-listed NAMES pass, so secrets carried under
// any other name are excluded by construction.
func sanitizedCommandEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if commandEnvAllowList[strings.ToLower(kv[:eq])] {
			out = append(out, kv)
		}
	}
	return out
}
