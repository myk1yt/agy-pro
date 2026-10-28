package providers

// Environment-variable name resolution with BOM/pollution tolerance.
//
// Live-incident background (qa-baseline-report.md B1/S1-S3): a valid provider
// key was stored in HKCU\Environment under a U+FEFF-prefixed spelling of its
// env-var name, while the exact name held a dead mock value. Exact-match
// registry lookups could therefore never see the real key. The helpers here
// normalize names (strip U+FEFF, trim whitespace), prefer exact matches, and
// fall back to BOM-polluted variants only when no exact source resolves.

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// BOM is the UTF-8 byte order mark / zero-width no-break space (U+FEFF) that
// text editors occasionally prepend to pasted environment-variable names.
const BOM = "\ufeff"

// Roots addressed by EnvStore. EnvRootProcess covers the current process block;
// EnvRootUser / EnvRootMachine are the persistent Windows environment stores
// (HKCU\Environment and HKLM\...\Session Manager\Environment).
const (
	EnvRootProcess = "process-env"
	EnvRootUser    = "user"
	EnvRootMachine = "machine"
)

// normalizeEnvName folds an environment-variable name to its clean form: every
// U+FEFF anywhere in the name is removed and surrounding whitespace is trimmed.
// "trim \ufeff" from the middle of a name is deliberate: a mid-name BOM is the
// same paste corruption and no legitimate variable name contains one.
func normalizeEnvName(name string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(name), BOM, ""))
}

// KeyOrigin reports where a resolved key value came from. The BOM-variant
// origins mean the value was found under a name that only normalizes to the
// requested variable name.
type KeyOrigin string

const (
	KeyOriginNone         KeyOrigin = ""
	KeyOriginProcessEnv   KeyOrigin = "process-env"
	KeyOriginProcessBOM   KeyOrigin = "process-env-bom-variant"
	KeyOriginUserExact    KeyOrigin = "hkcu-exact"
	KeyOriginUserBOM      KeyOrigin = "hkcu-bom-variant"
	KeyOriginMachineExact KeyOrigin = "hklm-exact"
	KeyOriginMachineBOM   KeyOrigin = "hklm-bom-variant"
)

// EnvStore abstracts the persistent Windows environment stores so key repair
// and lookup can be unit-tested against a mock. All methods are read/write; a
// read-only caller simply never calls Set/Delete.
type EnvStore interface {
	// Names lists every variable name stored under root.
	Names(root string) ([]string, error)
	// Get returns the value stored under (root, name). ok is false when the
	// variable does not exist.
	Get(root, name string) (value string, ok bool, err error)
	// Set creates or overwrites (root, name).
	Set(root, name, value string) error
	// Delete removes (root, name). Deleting a missing variable is not an error.
	Delete(root, name string) error
}

// defaultEnvStore is the process-wide store used by KeyValue and the exported
// candidate/conflict helpers. It is nil on non-Windows platforms; tests inject
// a mock and restore it via t.Cleanup.
var defaultEnvStore EnvStore

func init() {
	if runtime.GOOS == "windows" {
		defaultEnvStore = &windowsEnvStore{}
	}
}

// envSnapshotTTL bounds how long one registry enumeration is reused, so a
// key written by `--fix-keys` (or by hand) becomes visible quickly while
// repeated lookups do not re-read the hive on every call.
const envSnapshotTTL = 3 * time.Second

// registryBackend is the persistent environment store (HKCU\Environment and
// the HKLM Session Manager key). The production implementation is the
// advapi32 registry API (win32reg_windows.go); tests inject a fake so no unit
// test touches the real hive.
type registryBackend interface {
	enumerate(root string) (map[string]string, error)
	query(root, name string) (string, bool, error)
	set(root, name, value string) error
	delete(root, name string) error
}

// windowsEnvStore reads and writes the persistent Windows environment through
// the advapi32 registry API. Names and values travel as exact UTF-16 pointers
// (a leading U+FEFF in a name survives, which is why reg.exe output parsing
// was never an option) and never on a process command line: the previous
// powershell.exe path base64-encoded the key name and value into the argv,
// where process-list and command-line-auditing observers could decode them
// (security finding S-2).
type windowsEnvStore struct {
	mu       sync.Mutex
	snapshot map[string]map[string]string
	cachedAt map[string]time.Time
	// reg is the backend seam. nil selects the real win32 registry, which
	// refuses to run under tests.
	reg registryBackend
}

// backend resolves the registry backend, refusing the real store under tests.
func (s *windowsEnvStore) backend() (registryBackend, error) {
	if s.reg != nil {
		return s.reg, nil
	}
	if isRunningTests() {
		return nil, errors.New("windows environment store is disabled while running tests (registry reads/writes are skipped; inject a mock EnvStore)")
	}
	return win32Registry{}, nil
}

// validateRoot rejects roots the store does not back.
func validateEnvRoot(root string) error {
	if root != EnvRootUser && root != EnvRootMachine {
		return fmt.Errorf("unsupported environment root %q (want %q or %q)", root, EnvRootUser, EnvRootMachine)
	}
	return nil
}

// enumerate returns name -> value for one root, reusing a TTL-cached snapshot.
func (s *windowsEnvStore) enumerate(root string) (map[string]string, error) {
	if err := validateEnvRoot(root); err != nil {
		return nil, err
	}
	backend, err := s.backend()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.snapshot != nil && time.Since(s.cachedAt[root]) < envSnapshotTTL {
		out := s.snapshot[root]
		s.mu.Unlock()
		if out != nil {
			return out, nil
		}
	} else {
		s.mu.Unlock()
	}
	vars, err := backend.enumerate(root)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.snapshot == nil {
		s.snapshot = map[string]map[string]string{}
		s.cachedAt = map[string]time.Time{}
	}
	s.snapshot[root] = vars
	s.cachedAt[root] = time.Now()
	s.mu.Unlock()
	return vars, nil
}

func (s *windowsEnvStore) Names(root string) ([]string, error) {
	vars, err := s.enumerate(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(vars))
	for n := range vars {
		names = append(names, n)
	}
	return names, nil
}

func (s *windowsEnvStore) Get(root, name string) (string, bool, error) {
	vars, err := s.enumerate(root)
	if err != nil {
		return "", false, err
	}
	v, ok := vars[name]
	return v, ok, nil
}

func (s *windowsEnvStore) Set(root, name, value string) error {
	if err := validateEnvRoot(root); err != nil {
		return err
	}
	backend, err := s.backend()
	if err != nil {
		return err
	}
	if err := backend.set(root, name, value); err != nil {
		return err
	}
	s.invalidate(root)
	return nil
}

func (s *windowsEnvStore) Delete(root, name string) error {
	if err := validateEnvRoot(root); err != nil {
		return err
	}
	backend, err := s.backend()
	if err != nil {
		return err
	}
	if err := backend.delete(root, name); err != nil {
		return err
	}
	s.invalidate(root)
	return nil
}

func (s *windowsEnvStore) invalidate(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot != nil {
		delete(s.snapshot, root)
		delete(s.cachedAt, root)
	}
}

// resolveEnvValueWithStore resolves envName: exact process env first, then
// exact persistent entries (HKCU before HKLM), then polluted-name variants
// (process block, then HKCU, then HKLM). An exact match always wins, so a
// conflicting clean/BOM pair keeps resolving the exact value; the conflict is
// surfaced by AuditProviderKeys / FindEnvNameConflicts instead.
func resolveEnvValueWithStore(envName string, store EnvStore) (string, KeyOrigin, bool) {
	name := strings.TrimSpace(envName)
	if name == "" {
		return "", KeyOriginNone, false
	}
	if v, ok := os.LookupEnv(name); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v, KeyOriginProcessEnv, true
		}
	}
	if store == nil {
		return "", KeyOriginNone, false
	}
	for _, root := range []string{EnvRootUser, EnvRootMachine} {
		if v, ok, err := store.Get(root, name); err == nil && ok {
			if v = strings.TrimSpace(v); v != "" {
				return v, originForRoot(root, true), true
			}
		}
	}
	if v, ok := lookupProcessEnvVariant(name); ok {
		return v, KeyOriginProcessBOM, true
	}
	for _, root := range []string{EnvRootUser, EnvRootMachine} {
		names, err := store.Names(root)
		if err != nil {
			continue
		}
		for _, n := range names {
			if n == name || normalizeEnvName(n) != name {
				continue
			}
			if v, ok, err := store.Get(root, n); err == nil && ok {
				if v = strings.TrimSpace(v); v != "" {
					return v, originForRoot(root, false), true
				}
			}
		}
	}
	return "", KeyOriginNone, false
}

func originForRoot(root string, exact bool) KeyOrigin {
	switch {
	case root == EnvRootUser && exact:
		return KeyOriginUserExact
	case root == EnvRootUser:
		return KeyOriginUserBOM
	case root == EnvRootMachine && exact:
		return KeyOriginMachineExact
	default:
		return KeyOriginMachineBOM
	}
}

// lookupProcessEnvVariant scans the current process environment block for a
// variable whose normalized name matches. New shells inherit HKCU entries
// verbatim, so a BOM-prefixed registry variable reappears here under its
// polluted name after a reboot.
func lookupProcessEnvVariant(name string) (string, bool) {
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		n, v := kv[:i], kv[i+1:]
		if n == name || normalizeEnvName(n) != name {
			continue
		}
		if v = strings.TrimSpace(v); v != "" {
			return v, true
		}
	}
	return "", false
}

// EnvCandidate is one environment entry whose normalized name equals a target
// variable name. Value is the raw secret: callers must render it through
// MaskSecret and never log it.
type EnvCandidate struct {
	Root  string // EnvRootProcess, EnvRootUser, or EnvRootMachine
	Name  string // name exactly as stored
	Exact bool   // Name equals the requested variable name verbatim
	Value string
}

// processEnvCandidates collects process-block entries matching envName.
func processEnvCandidates(envName string) []EnvCandidate {
	name := strings.TrimSpace(envName)
	if name == "" {
		return nil
	}
	var out []EnvCandidate
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		n, v := kv[:i], kv[i+1:]
		if normalizeEnvName(n) != name || strings.TrimSpace(v) == "" {
			continue
		}
		out = append(out, EnvCandidate{Root: EnvRootProcess, Name: n, Exact: n == name, Value: strings.TrimSpace(v)})
	}
	return out
}

// resolveEnvCandidatesWithStore collects persistent (registry) entries matching
// envName across HKCU and HKLM.
func resolveEnvCandidatesWithStore(envName string, store EnvStore) []EnvCandidate {
	name := strings.TrimSpace(envName)
	if name == "" || store == nil {
		return nil
	}
	var out []EnvCandidate
	for _, root := range []string{EnvRootUser, EnvRootMachine} {
		names, err := store.Names(root)
		if err != nil {
			continue
		}
		for _, n := range names {
			if normalizeEnvName(n) != name {
				continue
			}
			v, ok, err := store.Get(root, n)
			if err != nil || !ok || strings.TrimSpace(v) == "" {
				continue
			}
			out = append(out, EnvCandidate{Root: root, Name: n, Exact: n == name, Value: strings.TrimSpace(v)})
		}
	}
	return out
}

// ResolveEnvCandidates returns every persistent Windows environment entry whose
// normalized name equals envName (candidate-list API for doctor/wizard conflict
// surfacing). On non-Windows platforms, or when the store is unavailable, the
// result is empty. Values are raw secrets: mask before printing.
func ResolveEnvCandidates(envName string) []EnvCandidate {
	return resolveEnvCandidatesWithStore(envName, defaultEnvStore)
}

// EnvNameConflict reports that both a clean-name entry and at least one
// polluted (BOM) variant exist for the same variable in the persistent stores.
// Which of the two a resolver picks is deterministic (exact first), but the
// coexistence itself is the data corruption `--fix-keys` repairs.
type EnvNameConflict struct {
	Env      string
	Exact    []EnvCandidate
	Variants []EnvCandidate
}

// FindEnvNameConflicts checks the given variable names (typically the
// APIKeyEnv values of a registry) and returns those where clean and BOM-variant
// entries coexist in the persistent Windows environment.
func FindEnvNameConflicts(envNames ...string) []EnvNameConflict {
	var out []EnvNameConflict
	seen := map[string]bool{}
	for _, e := range envNames {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		cands := ResolveEnvCandidates(e)
		var exact, variants []EnvCandidate
		for _, c := range cands {
			if c.Exact {
				exact = append(exact, c)
			} else {
				variants = append(variants, c)
			}
		}
		if len(exact) > 0 && len(variants) > 0 {
			out = append(out, EnvNameConflict{Env: e, Exact: exact, Variants: variants})
		}
	}
	return out
}
