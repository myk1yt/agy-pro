package agysettings

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agypro/internal/providers"
)

// settingsFilePerm is the mode of every file this package writes (settings,
// backups). Directories it creates use settingsDirPerm.
const (
	settingsFilePerm = 0o600
	settingsDirPerm  = 0o700
)

// Lock and backup tunables. They are package seams so tests can narrow the
// waits; production values are the ones below.
var (
	// lockAcquireWait bounds how long Open blocks on a live lock before
	// failing cleanly with ErrLocked.
	lockAcquireWait = 2 * time.Second

	// lockPollInterval is the retry granularity while waiting for a live lock.
	lockPollInterval = 20 * time.Millisecond

	// staleLockAge is the bounded age after which a lock left behind by a
	// crashed writer is broken. Hold a File only for its read-modify-write
	// window and Close it as soon as Write returns.
	staleLockAge = 1 * time.Hour
)

// Errors returned by the managed file API.
var (
	// ErrLocked means another writer holds the lockfile for this settings file.
	ErrLocked = errors.New("agysettings: settings file is locked by another writer")

	// ErrNoBackup means Rollback was called before any backup existed.
	ErrNoBackup = errors.New("agysettings: no backup to roll back to")

	// ErrGlobalSettingsPath means the caller asked for the protected global
	// settings file without the explicit override. Writing it would switch
	// every session, so the default refuses.
	ErrGlobalSettingsPath = errors.New("agysettings: refusing to touch the protected global settings path")
)

// globalOpenWarning receives the loud warning OpenGlobal emits, severity marker
// included. It is a seam so tests can capture it; production writes to stderr.
var globalOpenWarning = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// errGlobalSettingsPath builds the refusal. It names the path CLASS and never
// the resolved absolute path, so no user directory leaks into a log.
func errGlobalSettingsPath() error {
	return fmt.Errorf("%w (%s); profile P2 writes a per-session app-data-dir settings file instead (AppDataDirSettingsPath). Pass OpenOptions{AllowGlobal: true} or call OpenGlobal to override deliberately", ErrGlobalSettingsPath, globalSettingsPathShape)
}

// OpenOptions configures OpenWithOptions.
type OpenOptions struct {
	// AllowGlobal permits the handle to address the protected global settings
	// path. It defaults to false; the hybrid front doors never set it.
	AllowGlobal bool
}

// File is a handle on one managed settings.json.
//
// A File is exclusive: Open acquires the same-directory lockfile and holds it
// until Close, because the managed API is a read-modify-write cycle and two
// concurrent cycles would silently lose writes. Write re-verifies the lock
// before mutating, so a lock broken out from under a File is caught rather than
// written through; when that happens the document is re-read from disk and this
// handle's managed-key changes are re-applied on top, so a concurrent writer's
// content is never silently overwritten. The lock is released by truncation,
// never by unlink, and a lock left behind by a crashed writer is broken once it
// is older than staleLockAge.
type File struct {
	path string

	// allowGlobal records the explicit override for the protected global path.
	allowGlobal bool

	// raw is the whole document as read. Unmanaged keys are carried through
	// verbatim; managed keys are replaced from doc on every Write.
	raw map[string]json.RawMessage

	// doc is the managed view of the ledger keys.
	doc SettingsDoc

	owner      string
	lastBackup string
	backups    []string
	closed     bool
}

// Open reads the settings file at path and takes its lockfile.
//
// A missing file yields an empty document, not an error. A malformed file
// yields a clear error and is never overwritten: the caller decides what to do
// (roll back, repair, or abort). The parent directory is created if needed; it
// is the container of the target, and the only thing outside the target that a
// write cycle may touch.
//
// The protected global settings path is refused by default: see
// OpenWithOptions.
func Open(path string) (*File, error) {
	return OpenWithOptions(path, OpenOptions{})
}

// OpenWithOptions is Open with the explicit-override switch for the protected
// global settings path.
//
// By default a handle may not address <home>/.gemini/antigravity-cli/settings.json.
// That file belongs to every session at once, so a front door that wrote it
// would switch the user's whole environment, and it is a protected path. The
// refusal names the path class, not the resolved path. A per-session
// app-data-dir settings file (AppDataDirSettingsPath) is unaffected.
func OpenWithOptions(path string, opts OpenOptions) (_ *File, err error) {
	// Every error leaving a public entry point is scrubbed, so the no-local-path
	// rule holds by construction rather than by site-by-site review.
	defer func() { err = scrubErrorPaths(err) }()

	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("agysettings: settings path must not be empty")
	}
	clean := filepath.Clean(path)

	if !opts.AllowGlobal && isGlobalSettingsPath(clean) {
		return nil, errGlobalSettingsPath()
	}

	if dir := filepath.Dir(clean); dir != "" {
		if err := os.MkdirAll(dir, settingsDirPerm); err != nil {
			return nil, fmt.Errorf("agysettings: create directory %s: %w", maskedPath(dir, "the settings directory"), wrapOSError(err, "the settings directory"))
		}
	}

	owner, brokeStale, err := acquireLock(clean)
	if err != nil {
		return nil, err
	}

	f := &File{path: clean, allowGlobal: opts.AllowGlobal, owner: owner, raw: map[string]json.RawMessage{}}
	// load reads after the lock is held, so the document already reflects
	// whatever landed on disk during the acquire wait, including after a stale
	// lock was broken. brokeStale therefore needs no extra reload here.
	_ = brokeStale
	if err := f.load(); err != nil {
		// Do not hold the lock for a file we refused to read.
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// OpenGlobal opens the protected global settings path on purpose, with a loud
// warning. Nothing in the hybrid front doors calls it: it exists for an
// operator tool that must inspect or repair the global file, and the warning
// makes that choice visible in the log.
func OpenGlobal(path string) (_ *File, err error) {
	defer func() { err = scrubErrorPaths(err) }()
	globalOpenWarning("agysettings: WARNING: opening the protected global settings path (%s); the hybrid profiles must write a per-session app-data-dir settings file instead (AppDataDirSettingsPath)", globalSettingsPathShape)
	f, err := OpenWithOptions(path, OpenOptions{AllowGlobal: true})
	return f, err
}

// Path returns the settings file this handle owns.
func (f *File) Path() string { return f.path }

// load reads the document from disk. Missing and empty files are an empty
// document; anything unparseable is an error.
func (f *File) load() error {
	data, err := os.ReadFile(f.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agysettings: read %s: %w", maskedPath(f.path, "the settings file"), wrapOSError(err, "the settings file"))
		}
		f.raw = map[string]json.RawMessage{}
		return f.syncDocFromRaw()
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		f.raw = map[string]json.RawMessage{}
		return f.syncDocFromRaw()
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("agysettings: %s is malformed; refusing to overwrite it: %w", maskedPath(f.path, "the settings file"), err)
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	f.raw = raw
	return f.syncDocFromRaw()
}

// syncDocFromRaw decodes the managed keys out of the raw document. A managed
// key with the wrong shape is an error, because guessing would corrupt it.
func (f *File) syncDocFromRaw() error {
	f.doc = SettingsDoc{}
	if v, ok := f.raw[KeyModelProvider]; ok {
		if err := json.Unmarshal(v, &f.doc.ModelProvider); err != nil {
			return fmt.Errorf("agysettings: %s: key %q is not a string: %w", maskedPath(f.path, "the settings file"), KeyModelProvider, err)
		}
	}
	if v, ok := f.raw[KeyCustomModels]; ok {
		if err := json.Unmarshal(v, &f.doc.CustomModels); err != nil {
			return fmt.Errorf("agysettings: %s: key %q is not a customModels object: %w", maskedPath(f.path, "the settings file"), KeyCustomModels, err)
		}
		if err := validateCustomModels(f.doc.CustomModels); err != nil {
			return err
		}
	}
	return nil
}

// Get returns the current value of a managed key. A nil value with a nil error
// means the key is absent from the document. A key outside the ledger returns
// ErrNotManaged; this package never reads unmanaged keys through this API.
func (f *File) Get(key string) (any, error) {
	if !isManagedKey(key) {
		return nil, notManagedError(key)
	}
	v, present, err := f.docValue(key)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	return v, nil
}

// Set writes a managed key. value must match the ledger's shape for that key:
// a string for modelProvider, a map[string]CustomModelConfig for customModels.
// The whole key is owned by this package, so Set replaces it rather than
// merging. A zero value clears the key, exactly like Delete.
func (f *File) Set(key string, value any) error {
	if !isManagedKey(key) {
		return notManagedError(key)
	}
	switch ledger[key] {
	case kindString:
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("agysettings: key %q expects a string, got %T", key, value)
		}
		f.doc.ModelProvider = s
	case kindCustomModels:
		m, err := toCustomModels(value)
		if err != nil {
			return err
		}
		if err := validateCustomModels(m); err != nil {
			return err
		}
		f.doc.CustomModels = m
	}
	return nil
}

// Delete clears a managed key. The key is omitted from the next Write, which is
// the same as setting it to its zero value. Nothing is deleted from disk: the
// previous bytes are backed up first.
func (f *File) Delete(key string) error {
	if !isManagedKey(key) {
		return notManagedError(key)
	}
	switch ledger[key] {
	case kindString:
		f.doc.ModelProvider = ""
	case kindCustomModels:
		f.doc.CustomModels = nil
	}
	return nil
}

// Keys returns the managed keys currently present in the document, sorted.
func (f *File) Keys() []string {
	var out []string
	for _, key := range ManagedKeys() {
		if _, present, err := f.docValue(key); err == nil && present {
			out = append(out, key)
		}
	}
	return out
}

// LastBackup returns the path of the backup taken by the most recent Write, or
// "" when the last Write created no backup (the file did not exist).
func (f *File) LastBackup() string { return f.lastBackup }

// Backups returns every backup this handle created, oldest first.
func (f *File) Backups() []string { return append([]string(nil), f.backups...) }

// docValue returns the value a managed key contributes to the outgoing
// document, whether it is present at all, and any validation error.
func (f *File) docValue(key string) (any, bool, error) {
	switch key {
	case KeyModelProvider:
		return f.doc.ModelProvider, f.doc.ModelProvider != "", nil
	case KeyCustomModels:
		if len(f.doc.CustomModels) == 0 {
			return nil, false, nil
		}
		if err := validateCustomModels(f.doc.CustomModels); err != nil {
			return nil, false, err
		}
		return f.doc.CustomModels, true, nil
	}
	return nil, false, notManagedError(key)
}

// toCustomModels coerces a Set value into the managed map shape.
func toCustomModels(value any) (map[string]CustomModelConfig, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case map[string]CustomModelConfig:
		out := make(map[string]CustomModelConfig, len(v))
		for k, e := range v {
			out[k] = e
		}
		return out, nil
	default:
		return nil, fmt.Errorf("agysettings: key %q expects map[string]CustomModelConfig, got %T", KeyCustomModels, value)
	}
}

// Write persists the document: lockfile re-verification, ledger guard,
// timestamped backup of the previous bytes, atomic write.
//
// The outgoing document is the raw document as read, with the managed slots
// replaced. Unmanaged keys keep their original bytes; managed keys not present
// in the document are dropped.
//
// If the lock had to be taken over from another writer, the document is re-read
// from disk first and this handle's managed-key changes are re-applied on top,
// so a concurrent writer's content is never silently overwritten.
func (f *File) Write() (err error) {
	defer func() { err = scrubErrorPaths(err) }()
	if f.closed {
		return fmt.Errorf("agysettings: %s is closed", maskedPath(f.path, "the settings file"))
	}
	if err := f.refuseGlobalWrite(); err != nil {
		return err
	}
	relocked, err := f.ensureLock()
	if err != nil {
		return err
	}
	if relocked {
		if err := f.reloadPreservingManaged(); err != nil {
			return err
		}
	}
	if err := assertLedgerComplete(); err != nil {
		return err
	}

	out := make(map[string]json.RawMessage, len(f.raw)+len(ledger))
	for k, v := range f.raw {
		out[k] = v
	}
	for _, key := range ManagedKeys() {
		value, present, err := f.docValue(key)
		if err != nil {
			return err
		}
		if !present {
			delete(out, key)
			continue
		}
		enc, err := encodeJSON(value)
		if err != nil {
			return fmt.Errorf("agysettings: encode key %q: %w", key, err)
		}
		out[key] = json.RawMessage(enc)
	}

	data, err := encodeJSON(out)
	if err != nil {
		return fmt.Errorf("agysettings: encode %s: %w", maskedPath(f.path, "the settings file"), err)
	}

	// Timestamped backup of the previous bytes. A file that does not exist has
	// nothing to back up, and Rollback reports ErrNoBackup for that case.
	prev, err := os.ReadFile(f.path)
	switch {
	case err == nil && len(prev) > 0:
		backup, berr := writeBackup(f.path, prev)
		if berr != nil {
			return berr
		}
		f.lastBackup = backup
		f.backups = append(f.backups, backup)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("agysettings: read %s before write: %w", maskedPath(f.path, "the settings file"), wrapOSError(err, "the settings file"))
	}

	if err := providers.WriteFileAtomic(f.path, data, settingsFilePerm); err != nil {
		return fmt.Errorf("agysettings: write %s: %w", maskedPath(f.path, "the settings file"), wrapOSError(err, "the settings file"))
	}
	f.raw = out
	return nil
}

// Rollback restores the last backup byte-for-byte. It never deletes anything
// and never creates a new backup, so it is idempotent: rolling back twice
// writes the same bytes twice.
func (f *File) Rollback() (err error) {
	defer func() { err = scrubErrorPaths(err) }()
	if f.closed {
		return fmt.Errorf("agysettings: %s is closed", maskedPath(f.path, "the settings file"))
	}
	if err := f.refuseGlobalWrite(); err != nil {
		return err
	}
	if f.lastBackup == "" {
		return fmt.Errorf("%w: %s has no recorded backup (the file did not exist at the last Write)", ErrNoBackup, maskedPath(f.path, "the settings file"))
	}
	data, err := os.ReadFile(f.lastBackup)
	if err != nil {
		return fmt.Errorf("agysettings: read backup %s: %w", maskedPath(f.lastBackup, "the backup file"), wrapOSError(err, "the backup file"))
	}
	if _, err := f.ensureLock(); err != nil {
		return err
	}
	if err := providers.WriteFileAtomic(f.path, data, settingsFilePerm); err != nil {
		return fmt.Errorf("agysettings: restore %s from %s: %w", maskedPath(f.path, "the settings file"), maskedPath(f.lastBackup, "the backup file"), wrapOSError(err, "the settings file"))
	}
	return f.load()
}

// refuseGlobalWrite is the write-path half of the global-path guard. Open already
// refuses the protected path, but a handle built without Open (or a path that
// became the global path after a home change) must not be able to write either.
func (f *File) refuseGlobalWrite() error {
	if f.allowGlobal || !isGlobalSettingsPath(f.path) {
		return nil
	}
	return errGlobalSettingsPath()
}

// reloadPreservingManaged re-reads the document from disk and re-applies this
// handle's managed-key changes on top.
//
// It runs when the lock had to be taken over from another writer, which means
// content may have landed while this handle waited. Reloading alone would drop
// the caller's pending Set calls; writing the stale document alone would drop
// the other writer's content. Merging keeps both: a managed key the caller set
// wins, everything else comes from disk.
func (f *File) reloadPreservingManaged() error {
	intent := f.doc
	if err := f.load(); err != nil {
		return err
	}
	f.doc = mergeManaged(intent, f.doc)
	return nil
}

// mergeManaged overlays the caller's managed-key intent onto the freshly read
// document.
func mergeManaged(intent, onDisk SettingsDoc) SettingsDoc {
	out := onDisk
	if intent.ModelProvider != "" {
		out.ModelProvider = intent.ModelProvider
	}
	if len(intent.CustomModels) > 0 {
		out.CustomModels = intent.CustomModels
	}
	return out
}

// Close releases the lockfile. The lock file is truncated, never unlinked, so a
// released lock is an empty file the next writer takes over in place.
func (f *File) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	lock := lockPath(f.path)
	if owned, err := lockOwnedBy(lock, f.owner); err != nil || !owned {
		// Someone else owns it now (it was broken as stale): leave it alone.
		return nil
	}
	// The release is an O_TRUNC open, so it must not follow a symlink that
	// appeared at the lock path after this handle acquired it.
	if err := refuseNonRegularLock(lock); err != nil {
		return err
	}
	fh, err := os.OpenFile(lock, os.O_WRONLY|os.O_TRUNC, settingsFilePerm)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("agysettings: release lock %s: %w", maskedPath(lock, "the lock file"), wrapOSError(err, "the lock file"))
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("agysettings: release lock %s: %w", maskedPath(lock, "the lock file"), wrapOSError(err, "the lock file"))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Lockfile
// ---------------------------------------------------------------------------

// lockPath returns the lockfile sibling of a settings path. It is a pure string
// suffix, so the lock can never leave the target's directory.
func lockPath(target string) string { return target + ".lock" }

// writeBackup copies prev to <target>.bak.YYYYMMDD-HHMMSS. The documented name
// is used as-is; a collision inside the same second gets a -2, -3, ... suffix
// so an existing backup is never overwritten.
func writeBackup(target string, prev []byte) (string, error) {
	base := target + ".bak." + time.Now().Format("20060102-150405")
	name := base
	for i := 2; ; i++ {
		_, err := os.Stat(name)
		if err == nil {
			name = fmt.Sprintf("%s-%d", base, i)
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("agysettings: check backup %s: %w", maskedPath(name, "the backup file"), wrapOSError(err, "the backup file"))
		}
		break
	}
	if err := providers.WriteFileAtomic(name, prev, settingsFilePerm); err != nil {
		return "", fmt.Errorf("agysettings: backup %s: %w", maskedPath(name, "the backup file"), wrapOSError(err, "the backup file"))
	}
	return name, nil
}

// ownerToken is a per-File lock identity: pid plus random bytes, so a recycled
// pid cannot be mistaken for the same writer.
func ownerToken() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("agysettings: lock token: %w", err)
	}
	return fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(raw[:])), nil
}

// lockContent is the parsed lockfile body.
type lockContent struct {
	pid      string
	acquired time.Time
	owner    string
}

func (c lockContent) encode() []byte {
	return []byte(fmt.Sprintf("pid=%s\nacquired=%s\nowner=%s\n", c.pid, c.acquired.UTC().Format(time.RFC3339Nano), c.owner))
}

func parseLockContent(data []byte) (lockContent, bool) {
	var c lockContent
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "pid":
			c.pid = strings.TrimSpace(value)
		case "owner":
			c.owner = strings.TrimSpace(value)
		case "acquired":
			at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
			if err != nil {
				return lockContent{}, false
			}
			c.acquired = at
		}
	}
	if c.owner == "" || c.acquired.IsZero() {
		return lockContent{}, false
	}
	return c, true
}

// refuseNonRegularLock fails when the lock path exists and is not a regular
// file.
//
// The takeover and release paths open the lock with O_TRUNC, which FOLLOWS a
// symlink. A planted symlink at <target>.lock would therefore truncate and
// rewrite whatever it points at (it cannot redirect the settings write itself,
// because the settings path is never opened through the lock, but it corrupts an
// unrelated file). Lstat does not follow the link, so the check sees the link
// for what it is. It also covers a directory or a device node planted at the
// lock path.
func refuseNonRegularLock(lock string) error {
	info, err := os.Lstat(lock)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("agysettings: inspect lock %s: %w", maskedPath(lock, "the lock file"), wrapOSError(err, "the lock file"))
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("agysettings: refusing to use lock %s: it is a %s, not a regular file", maskedPath(lock, "the lock file"), info.Mode().Type())
	}
	return nil
}

// acquireLock creates the lockfile for target, waiting at most lockAcquireWait
// for a live lock and breaking a lock older than staleLockAge. It never
// unlinks: a stale or released lock is taken over in place by truncation.
//
// The second return value reports whether an existing lock was taken over
// rather than freshly created. A takeover means another writer may have been
// mid-cycle, so the caller must re-read the target before writing.
func acquireLock(target string) (string, bool, error) {
	lock := lockPath(target)
	token, err := ownerToken()
	if err != nil {
		return "", false, err
	}
	deadline := time.Now().Add(lockAcquireWait)
	for {
		if err := refuseNonRegularLock(lock); err != nil {
			return "", false, err
		}
		fh, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, settingsFilePerm)
		if err == nil {
			content := lockContent{pid: fmt.Sprint(os.Getpid()), acquired: time.Now(), owner: token}
			if _, werr := fh.Write(content.encode()); werr != nil {
				_ = fh.Close()
				return "", false, fmt.Errorf("agysettings: write lock %s: %w", maskedPath(lock, "the lock file"), wrapOSError(werr, "the lock file"))
			}
			if cerr := fh.Close(); cerr != nil {
				return "", false, fmt.Errorf("agysettings: close lock %s: %w", maskedPath(lock, "the lock file"), wrapOSError(cerr, "the lock file"))
			}
			return token, false, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", false, fmt.Errorf("agysettings: create lock %s: %w", maskedPath(lock, "the lock file"), wrapOSError(err, "the lock file"))
		}

		data, rerr := os.ReadFile(lock)
		if rerr != nil {
			if errors.Is(rerr, os.ErrNotExist) {
				// Released between the create attempt and the read. Bound the
				// retry so a lock that keeps vanishing cannot spin forever.
				if !time.Now().Before(deadline) {
					return "", false, fmt.Errorf("%w: %s (kept vanishing while acquiring)", ErrLocked, maskedPath(lock, "the lock file"))
				}
				continue
			}
			return "", false, fmt.Errorf("agysettings: read lock %s: %w", maskedPath(lock, "the lock file"), wrapOSError(rerr, "the lock file"))
		}

		content, ok := parseLockContent(data)
		switch {
		case len(strings.TrimSpace(string(data))) == 0:
			// Released lock (Close truncated it): take it over.
		case !ok:
			// Unparseable body: a crash mid-write. Fall back to the file's age.
			if info, serr := os.Stat(lock); serr == nil && time.Since(info.ModTime()) < staleLockAge {
				if !time.Now().Before(deadline) {
					return "", false, lockedError(lock, "unknown", time.Time{}, time.Since(info.ModTime()))
				}
				time.Sleep(lockPollInterval)
				continue
			}
		default:
			if age := time.Since(content.acquired); age < staleLockAge {
				if !time.Now().Before(deadline) {
					return "", false, lockedError(lock, content.pid, content.acquired, age)
				}
				time.Sleep(lockPollInterval)
				continue
			}
		}

		if err := takeOverLock(lock, token); err != nil {
			// Another writer won the race; the next iteration re-reads it.
			if !time.Now().Before(deadline) {
				return "", false, fmt.Errorf("%w: %s (could not break a stale lock: %v)", ErrLocked, maskedPath(lock, "the lock file"), wrapOSError(err, "the lock file"))
			}
			time.Sleep(lockPollInterval)
			continue
		}
		return token, true, nil
	}
}

// lockedError builds the ErrLocked error with the holder's identity.
func lockedError(lock, pid string, since time.Time, age time.Duration) error {
	if since.IsZero() {
		return fmt.Errorf("%w: %s (held by pid %s for %s; a lock older than %s is broken)", ErrLocked, maskedPath(lock, "the lock file"), pid, age.Round(time.Second), staleLockAge)
	}
	return fmt.Errorf("%w: %s (held by pid %s since %s, %s ago; a lock older than %s is broken)", ErrLocked, maskedPath(lock, "the lock file"), pid, since.Format(time.RFC3339), age.Round(time.Second), staleLockAge)
}

// takeOverLock rewrites the lockfile in place with our token. It never unlinks,
// and it refuses a non-regular lock file so a planted symlink is never followed
// by the O_TRUNC open.
func takeOverLock(lock, token string) error {
	if err := refuseNonRegularLock(lock); err != nil {
		return err
	}
	fh, err := os.OpenFile(lock, os.O_WRONLY|os.O_TRUNC, settingsFilePerm)
	if err != nil {
		return err
	}
	content := lockContent{pid: fmt.Sprint(os.Getpid()), acquired: time.Now(), owner: token}
	if _, err := fh.Write(content.encode()); err != nil {
		_ = fh.Close()
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	// Confirm the takeover: if another writer rewrote the file in the race
	// window, our token is gone and the caller must retry.
	owned, err := lockOwnedBy(lock, token)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("%s was taken over by another writer", maskedPath(lock, "the lock file"))
	}
	return nil
}

// lockOwnedBy reports whether the lockfile currently carries token.
func lockOwnedBy(lock, token string) (bool, error) {
	data, err := os.ReadFile(lock)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("agysettings: read lock %s: %w", maskedPath(lock, "the lock file"), wrapOSError(err, "the lock file"))
	}
	content, ok := parseLockContent(data)
	if !ok {
		return false, nil
	}
	return content.owner == token, nil
}

// ensureLock re-verifies that this handle still owns the lockfile, re-acquiring
// it if it was released or broken. The boolean reports whether the lock had to
// be taken over from another writer, in which case the caller must re-read the
// target before writing: content may have landed while this handle waited.
func (f *File) ensureLock() (bool, error) {
	if owned, err := lockOwnedBy(lockPath(f.path), f.owner); err != nil {
		return false, err
	} else if owned {
		return false, nil
	}
	owner, brokeStale, err := acquireLock(f.path)
	if err != nil {
		return false, err
	}
	f.owner = owner
	return brokeStale, nil
}

// siblingPaths returns every path a write cycle may touch for target: the
// target itself, its lockfile, and the backup prefix. All are string-suffixed
// siblings, so nothing outside filepath.Dir(target) is reachable.
func siblingPaths(target string) []string {
	return []string{target, lockPath(target), target + ".bak."}
}
