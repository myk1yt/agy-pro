package agenttools

import (
	"bufio"
	"bytes"
	"context"
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
)

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

	err = filepath.WalkDir(safePath, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			name := d.Name()
			if d.IsDir() && (name == ".git" || name == "node_modules" || name == ".gemini") {
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

		f, err := os.Open(p)
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
		return "", fmt.Errorf("grep walk error: %w", err)
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
		if err != nil || d.IsDir() {
			name := d.Name()
			if d.IsDir() && (name == ".git" || name == "node_modules") {
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

	if !overwrite {
		if _, err := os.Stat(safePath); err == nil {
			return "", fmt.Errorf("file already exists: %s (set overwrite=true to overwrite)", path)
		}
	}

	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-write-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	data := []byte(content)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", err
	}

	if err := os.Rename(tmpName, safePath); err != nil {
		cleanup()
		return "", fmt.Errorf("failed to replace destination: %w", err)
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(data), path), nil
}

// RunCommand executes a command within root directory.
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
