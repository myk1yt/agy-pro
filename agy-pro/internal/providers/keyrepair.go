package providers

// `--fix-keys` repair: rewrite BOM-polluted key names onto their clean names
// after proving (via upstream probes) that the clean name holds a dead value
// and the polluted variant holds a valid one. Every mutation is preceded by a
// DPAPI-encrypted backup of the touched entries and is reversible through
// RollbackWindowsKeyNames.
//
// Safety rails:
//   - Replacement happens ONLY when the clean name probes invalid (or is
//     absent) AND a BOM variant probes valid. A valid clean value is never
//     touched, and a network-blocked probe skips the target instead of
//     guessing.
//   - Only HKCU (user) entries are mutated; HKLM entries are read-only.
//   - The real store refuses to run under `go test` (same skip pattern as the
//     wizard's registry persistence), so unit tests exercise the logic against
//     a mock store only.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// RepairAction names what the repair did to one target.
type RepairAction string

const (
	// RepairActionRewrite: the clean name now holds the probe-valid value that
	// previously lived under a BOM-polluted name.
	RepairActionRewrite RepairAction = "rewrote-clean-name"
	// RepairActionRemoveVariant: the promoted BOM-variant entry was deleted so
	// the clean/BOM conflict cannot resurface.
	RepairActionRemoveVariant RepairAction = "removed-bom-variant"
)

// RepairEntry is the per-target outcome in a RepairReport.
type RepairEntry struct {
	Provider    string       `json:"provider"`
	Env         string       `json:"env"`
	Action      RepairAction `json:"action,omitempty"`
	VariantName string       `json:"variantName,omitempty"`
	MaskedValue string       `json:"maskedValue,omitempty"`
	Note        string       `json:"note,omitempty"`
}

// RepairReport summarizes one repair run. It contains no secret values, only
// masked hints and the backup location.
type RepairReport struct {
	BackupPath string        `json:"backupPath"`
	Entries    []RepairEntry `json:"entries"`
	Repaired   int           `json:"repaired"`
	Skipped    int           `json:"skipped"`
	Failed     int           `json:"failed"`
}

// RepairOptions configures RepairWindowsKeyNames / RollbackWindowsKeyNames.
// Store and Probe are test seams (nil selects the real store and
// ProbeKeyValidity); BackupDir and Now exist for deterministic tests.
type RepairOptions struct {
	// EnvNames restricts the repair to these variable names. Empty means every
	// provider env name in the registry.
	EnvNames []string
	// Store defaults to the real Windows environment store, which refuses to
	// run under tests.
	Store EnvStore
	// Probe defaults to ProbeKeyValidity.
	Probe func(baseURL, apiKey string) KeyProbeResult
	// BackupDir defaults to ~/.agy-pro/backups/<YYYYMMDD_HHMMSS>.
	BackupDir string
	// Now defaults to time.Now; used for the backup directory name.
	Now func() time.Time
}

// backup file layout. Values are encrypted at rest with Windows DPAPI
// (user scope, CryptProtectData) before they are written: a stolen or
// roaming-synced copy of the file is useless to another account (security
// finding S-5; the previous plaintext layout leaned on a 0600 mode that does
// not map to an ACL on Windows). The file still lives under
// ~/.agy-pro/backups and must never be committed or logged.
//
// Format version 2 (current) stores each value as a base64 DPAPI blob and
// records "version": 2 plus a filename carrying the same version. Legacy
// version 1 files (registry-keys.json, plaintext values, no version field) are
// still readable for rollback.
const (
	backupFormatVersion = 2
	backupFileNameV2    = "registry-keys.v2.json"
	backupFileNameV1    = "registry-keys.json" // legacy plaintext layout
	// backupRetention is how many key-backup directories are kept; older ones
	// are pruned after every write so plaintext-era and DPAPI copies do not
	// accumulate without bound.
	backupRetention = 10
)

// repairToolName is the provenance stamp written into every backup file and
// verified on rollback, so an arbitrary JSON file is never applied as trusted
// repair instructions (security finding S-9).
const repairToolName = "agypro/internal/providers.RepairWindowsKeyNames"

// SecretProtector encrypts and decrypts backup values at rest. The production
// implementation is Windows DPAPI (dpapi_windows.go); tests inject a fake so
// no unit test needs the real user key.
type SecretProtector interface {
	Protect(plaintext []byte) ([]byte, error)
	Unprotect(ciphertext []byte) ([]byte, error)
}

// backupProtector is the process-wide protector used by the repair backup
// path. Tests swap it and restore it via t.Cleanup, the same pattern as
// defaultEnvStore.
var backupProtector SecretProtector = dpapiProtector{}

type keyBackupEntry struct {
	Root    string `json:"root"` // EnvRootUser, EnvRootMachine, or EnvRootProcess
	Name    string `json:"name"`
	Existed bool   `json:"existed"`
	// Value holds the protected (version 2) or plaintext (legacy version 1)
	// value; it is empty when Existed is false.
	Value string `json:"value,omitempty"`
}

type keyBackupFile struct {
	Version   int              `json:"version,omitempty"`
	CreatedAt string           `json:"createdAt"`
	Tool      string           `json:"tool"`
	Entries   []keyBackupEntry `json:"entries"`
}

// repairPlan is the evaluated intent for one target before any mutation.
type repairPlan struct {
	env           string
	provider      string
	baseURL       string
	clean         *EnvCandidate // user-root exact entry; nil when absent
	variant       EnvCandidate  // promoted variant (meaningful when rewrite)
	rewrite       bool
	deleteVariant bool
	note          string
}

// ResolveRepairTargets is the string-argument handler for the CLI: it turns the
// raw --fix-keys argument ("", "all", or a comma/space/semicolon-separated list
// of provider names or env-var names) into the deduplicated env-name target
// list used by RepairWindowsKeyNames.
func ResolveRepairTargets(reg *Registry, arg string) ([]string, error) {
	if reg == nil {
		return nil, errors.New("nil registry")
	}
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.EqualFold(arg, "all") {
		var out []string
		seen := map[string]bool{}
		for _, p := range reg.Providers {
			e := strings.TrimSpace(p.APIKeyEnv)
			if e == "" || seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
		}
		if len(out) == 0 {
			return nil, errors.New("no provider in the registry declares an API key variable")
		}
		return out, nil
	}
	tokens := strings.FieldsFunc(arg, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	if len(tokens) == 0 {
		return nil, fmt.Errorf("cannot parse repair target argument %q", MaskOperand(arg))
	}
	var out []string
	seen := map[string]bool{}
	for _, tok := range tokens {
		if seen[strings.ToLower(tok)] {
			continue
		}
		seen[strings.ToLower(tok)] = true
		if p, ok := reg.Provider(tok); ok {
			if strings.TrimSpace(p.APIKeyEnv) == "" {
				return nil, fmt.Errorf("provider %q declares no API key variable; nothing to repair", MaskOperand(p.Name))
			}
			out = append(out, strings.TrimSpace(p.APIKeyEnv))
			continue
		}
		found := false
		for _, p := range reg.Providers {
			if strings.TrimSpace(p.APIKeyEnv) == tok {
				out = append(out, tok)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%q matches no provider name or known API key variable", MaskOperand(tok))
		}
	}
	return out, nil
}

// RepairWindowsKeyNames evaluates every target, writes one plaintext backup of
// all values it is about to touch, and only then mutates the user environment:
// the clean name is rewritten with the probe-valid value from a BOM variant and
// the promoted variant entry is removed. Targets whose precondition is not met
// are skipped with a note; nothing is mutated when the backup cannot be
// written.
func RepairWindowsKeyNames(reg *Registry, opts RepairOptions) (*RepairReport, error) {
	if reg == nil {
		return nil, errors.New("nil registry")
	}
	store, err := repairStore(opts.Store)
	if err != nil {
		return nil, err
	}
	probe := opts.Probe
	if probe == nil {
		probe = ProbeKeyValidity
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	targets := opts.EnvNames
	if len(targets) == 0 {
		targets, err = ResolveRepairTargets(reg, "all")
		if err != nil {
			return nil, err
		}
	}

	report := &RepairReport{}
	var plans []*repairPlan
	for _, env := range targets {
		env = strings.TrimSpace(env)
		if env == "" {
			continue
		}
		plan := evaluateRepairTarget(reg, env, store, probe)
		plans = append(plans, plan)
	}

	var toApply []*repairPlan
	for _, p := range plans {
		if p.rewrite {
			toApply = append(toApply, p)
		}
	}

	if len(toApply) > 0 {
		backupPath, err := writeRepairBackup(toApply, opts.BackupDir, now)
		if err != nil {
			return nil, fmt.Errorf("aborting before any change: %w", err)
		}
		report.BackupPath = backupPath
	}

	var applyErrs []error
	for _, p := range plans {
		if !p.rewrite {
			report.Skipped++
			report.Entries = append(report.Entries, RepairEntry{
				Provider: p.provider, Env: p.env, Note: p.note,
			})
			continue
		}
		entry := RepairEntry{
			Provider:    p.provider,
			Env:         p.env,
			VariantName: p.variant.Name,
			MaskedValue: MaskSecret(p.variant.Value),
		}
		if err := store.Set(EnvRootUser, p.env, p.variant.Value); err != nil {
			report.Failed++
			entry.Note = fmt.Sprintf("rewrite failed: %v", err)
			report.Entries = append(report.Entries, entry)
			// E6: the env name resolves from the user-editable registry and a
			// misdirected paste can make it key-shaped; mask the prefix (the
			// CLI's fix-keys tail additionally redacts the wrapped error with
			// a redactor seeded with the target names).
			applyErrs = append(applyErrs, fmt.Errorf("%s: %w", MaskOperand(p.env), err))
			continue
		}
		entry.Action = RepairActionRewrite
		if p.deleteVariant {
			if err := store.Delete(EnvRootUser, p.variant.Name); err != nil {
				entry.Note = fmt.Sprintf("clean name rewritten, but removing the BOM variant failed: %v", err)
				applyErrs = append(applyErrs, fmt.Errorf("%s: remove variant: %w", MaskOperand(p.env), err))
			} else {
				report.Entries = append(report.Entries, RepairEntry{
					Provider:    p.provider,
					Env:         p.env,
					Action:      RepairActionRemoveVariant,
					VariantName: p.variant.Name,
					MaskedValue: MaskSecret(p.variant.Value),
				})
			}
		}
		// Best-effort process refresh so the running command (and the bridge)
		// pick up the repaired value without restarting.
		_ = os.Setenv(p.env, p.variant.Value)
		for _, c := range processEnvCandidates(p.env) {
			if !c.Exact {
				_ = os.Unsetenv(c.Name)
			}
		}
		report.Repaired++
		report.Entries = append(report.Entries, entry)
	}
	return report, errors.Join(applyErrs...)
}

// evaluateRepairTarget decides whether one env name qualifies for repair,
// following the strict replacement condition: the clean name must be absent or
// probe INVALID, and a BOM variant must probe VALID. Network-blocked probes
// yield a skip, not a mutation.
func evaluateRepairTarget(reg *Registry, env string, store EnvStore, probe func(baseURL, apiKey string) KeyProbeResult) *repairPlan {
	plan := &repairPlan{env: env}
	for _, p := range reg.Providers {
		if strings.TrimSpace(p.APIKeyEnv) == env {
			plan.provider = p.Name
			plan.baseURL = p.BaseURL
			break
		}
	}
	cands := resolveEnvCandidatesWithStore(env, store)
	for i := range cands {
		c := cands[i]
		if c.Root != EnvRootUser {
			continue
		}
		if c.Exact {
			cc := cands[i]
			plan.clean = &cc
		} else {
			plan.variant = c
		}
	}

	if plan.clean == nil {
		// Clean name absent: promote a probe-valid variant so the key lives
		// under the name the registry declares.
		for _, c := range cands {
			if c.Root == EnvRootUser && !c.Exact && probe(plan.baseURL, c.Value).Status == ProbeValid {
				plan.variant = c
				plan.rewrite = true
				plan.deleteVariant = true
				plan.note = "clean-name entry absent; promoted probe-valid BOM-variant value"
				return plan
			}
		}
		plan.note = "clean-name entry absent and no probe-valid BOM variant found; nothing to rewrite"
		return plan
	}

	probeClean := probe(plan.baseURL, plan.clean.Value)
	switch probeClean.Status {
	case ProbeValid:
		plan.note = "clean-name value already validates upstream; nothing to fix"
		return plan
	case ProbeSkipped, ProbeIndeterminate:
		plan.note = fmt.Sprintf("cannot verify the clean-name value (probe %s: %s); nothing changed", probeClean.Status, probeClean.Detail)
		return plan
	case ProbeInvalid:
		// fall through to variant search
	default:
		plan.note = fmt.Sprintf("unexpected probe status %q; nothing changed", probeClean.Status)
		return plan
	}

	for _, c := range cands {
		if c.Root != EnvRootUser || c.Exact || c.Value == plan.clean.Value {
			continue
		}
		if probe(plan.baseURL, c.Value).Status == ProbeValid {
			plan.variant = c
			plan.rewrite = true
			plan.deleteVariant = true
			plan.note = ""
			return plan
		}
	}
	plan.note = "clean-name value is invalid but no BOM variant carries a probe-valid value; nothing changed"
	return plan
}

func repairStore(override EnvStore) (EnvStore, error) {
	if override != nil {
		return override, nil
	}
	if isRunningTests() {
		return nil, errors.New("refusing to touch the real Windows environment while running tests; inject a mock EnvStore")
	}
	if defaultEnvStore == nil {
		return nil, errors.New("persistent environment repair is only supported on windows")
	}
	return defaultEnvStore, nil
}

// writeRepairBackup snapshots every value the plans will touch (user-root
// entries plus the current process env state of the clean names) into
// <BackupDir>/registry-keys.v2.json with each value DPAPI-protected (S-5).
// It returns the file path. When the caller leaves BackupDir empty, old key
// backup directories are pruned to backupRetention after the write.
func writeRepairBackup(plans []*repairPlan, backupDir string, now func() time.Time) (string, error) {
	protect := backupProtector
	if protect == nil {
		protect = dpapiProtector{}
	}
	defaultBase := ""
	if backupDir == "" {
		home, err := Home()
		if err != nil {
			return "", err
		}
		// Same-second runs must not share (and thereby overwrite) one backup
		// directory: the suffix pattern matches mode.uniqueBackupDir.
		defaultBase = filepath.Join(home, "backups")
		backupDir = uniqueBackupDir(filepath.Join(defaultBase, now().Format("20060102_150405")))
	}
	var entries []keyBackupEntry
	seen := map[string]bool{}
	addValue := func(root, name, value string, existed bool) (string, error) {
		stored := ""
		if existed && value != "" {
			blob, err := protect.Protect([]byte(value))
			if err != nil {
				return "", fmt.Errorf("protecting backup value for %s: %w", name, err)
			}
			stored = base64.StdEncoding.EncodeToString(blob)
		}
		entries = append(entries, keyBackupEntry{Root: root, Name: name, Existed: existed, Value: stored})
		return stored, nil
	}
	for _, p := range plans {
		envKey := p.env
		if !seen[envKey+"|"+EnvRootUser] {
			seen[envKey+"|"+EnvRootUser] = true
			if p.clean != nil {
				if _, err := addValue(EnvRootUser, p.env, p.clean.Value, true); err != nil {
					return "", err
				}
			} else {
				entries = append(entries, keyBackupEntry{Root: EnvRootUser, Name: p.env, Existed: false})
			}
		}
		vk := p.variant.Name + "|" + EnvRootUser
		if p.deleteVariant && p.variant.Name != "" && !seen[vk] {
			seen[vk] = true
			if _, err := addValue(EnvRootUser, p.variant.Name, p.variant.Value, true); err != nil {
				return "", err
			}
		}
		pk := p.env + "|" + EnvRootProcess
		if !seen[pk] {
			seen[pk] = true
			if prior, ok := os.LookupEnv(p.env); ok {
				if _, err := addValue(EnvRootProcess, p.env, prior, true); err != nil {
					return "", err
				}
			} else {
				entries = append(entries, keyBackupEntry{Root: EnvRootProcess, Name: p.env, Existed: false})
			}
		}
	}
	file := keyBackupFile{
		Version:   backupFormatVersion,
		CreatedAt: now().UTC().Format(time.RFC3339),
		Tool:      repairToolName,
		Entries:   entries,
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	path := filepath.Join(backupDir, backupFileNameV2)
	if err := WriteFileAtomic(path, data, 0o600); err != nil {
		return "", err
	}
	if defaultBase != "" {
		// Retention (S-5): keep only the newest backupRetention key backups.
		// Best-effort - a pruning failure must not fail the repair itself.
		_ = pruneOldKeyBackups(defaultBase)
	}
	return path, nil
}

// pruneOldKeyBackups keeps the newest backupRetention directories under base
// that contain a key backup file (either format) and deletes the rest. Other
// backup directories (for example the mode profile backups sharing the same
// parent) are never touched.
func pruneOldKeyBackups(base string) error {
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if _, err := os.Stat(filepath.Join(dir, backupFileNameV2)); err == nil {
			dirs = append(dirs, dir)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, backupFileNameV1)); err == nil {
			dirs = append(dirs, dir)
		}
	}
	// Directory names are timestamp-first, so lexical order is chronological
	// (the uniqueBackupDir _2/_3 suffixes sort right after their base name).
	sort.Strings(dirs)
	if len(dirs) <= backupRetention {
		return nil
	}
	var errs []error
	for _, dir := range dirs[:len(dirs)-backupRetention] {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("pruning old key backup %s: %w", dir, err))
		}
	}
	return errors.Join(errs...)
}

// resolveBackupFile accepts either the backup directory printed by
// --fix-keys or a direct file path, preferring the versioned file over the
// legacy plaintext one.
func resolveBackupFile(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if fi, err := os.Stat(target); err == nil && fi.IsDir() {
		for _, name := range []string{backupFileNameV2, backupFileNameV1} {
			p := filepath.Join(target, name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		return filepath.Join(target, backupFileNameV2)
	}
	return target
}

// ResolveBackupFile is the exported form used by the CLI's --rollback-keys
// argument handling.
func ResolveBackupFile(target string) string {
	return resolveBackupFile(target)
}

// uniqueBackupDir returns path with a numeric suffix appended when it already
// exists, so two repair runs within the same second never share (and thereby
// overwrite) one backup directory. The suffix keeps lexical order aligned with
// chronological order, matching mode.uniqueBackupDir.
func uniqueBackupDir(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", path, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// rollbackDeniedNames are environment variables that grant code execution,
// search-path control or tool redirection when written or deleted. A backup
// file is never allowed to touch them, however it was obtained (S-9).
var rollbackDeniedNames = map[string]bool{
	"path": true, "pathext": true, "comspec": true,
	"systemroot": true, "systemdrive": true, "windir": true,
	"temp": true, "tmp": true, "tmpdir": true,
	"home": true, "userprofile": true, "homedrive": true, "homepath": true,
	"appdata": true, "localappdata": true, "programdata": true,
	"programfiles": true, "programfiles(x86)": true, "programw6432": true,
	"psmodulepath": true, "prompt": true,
	"node_options": true, "node_path": true, "node_extra_ca_certs": true,
	"pythonstartup": true, "pythonpath": true, "pythonhome": true,
	"perl5opt": true, "perl5lib": true, "rubyopt": true, "rubylib": true,
	"java_tool_options": true, "_java_options": true, "javahome": true, "java_home": true,
	"git_ssh_command": true, "ssh_askpass": true, "git_askpass": true,
	"ld_preload": true, "ld_library_path": true, "dyld_insert_libraries": true,
	"classpath": true, "gopath": true, "goroot": true,
	"requests_ca_bundle": true, "ssl_cert_file": true, "curl_ca_bundle": true,
	"browser": true, "editor": true, "visual": true,
	"agy_pro_real_agy": true, "agy_pro_providers": true, "agy_pro_home": true,
	"agy_pro_bridge": true, "agy_bridge": true, "agy_launcher": true,
}

// keyNameShape matches credential-style variable names - the only class of
// names a key backup may restore ("key names only", S-9).
var keyNameShape = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSW|CRED)`)

// validateBackupEntry enforces the rollback input whitelist (S-9): only
// credential-shaped names in the user or process root may be restored or
// removed. PATH/NODE_OPTIONS-class names and machine-root writes are refused
// outright, so a crafted registry-keys.json cannot turn "restore my backup"
// into persistent environment hijacking.
func validateBackupEntry(e keyBackupEntry) error {
	name := normalizeEnvName(e.Name)
	if name == "" {
		return errors.New("backup entry with empty name")
	}
	// F7: the refusal sentences echo backup-file-derived names (S-9 untrusted
	// input); a crafted file can plant a credential in the name slot, so every
	// echo routes through MaskSentence seeded with the raw + normalized name.
	// Benign names stay readable.
	if e.Root != EnvRootUser && e.Root != EnvRootProcess {
		return errors.New(MaskSentence(fmt.Sprintf("entry %q: root %q is not allowed on rollback (only %q and %q)", name, e.Root, EnvRootUser, EnvRootProcess), e.Name, name))
	}
	if rollbackDeniedNames[strings.ToLower(name)] {
		return errors.New(MaskSentence(fmt.Sprintf("entry %q: name is on the rollback deny-list (PATH/NODE_OPTIONS-class variables are never restored)", name), e.Name, name))
	}
	if !keyNameShape.MatchString(name) {
		return errors.New(MaskSentence(fmt.Sprintf("entry %q: name does not look like an API key variable (want KEY/TOKEN/SECRET/PASSWORD/CREDENTIAL in the name)", name), e.Name, name))
	}
	return nil
}

// decodeBackupValue turns a stored entry value back into plaintext: base64
// DPAPI blobs for version 2, raw text for the legacy version 1 layout.
func decodeBackupValue(version int, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if version < backupFormatVersion {
		return stored, nil // legacy plaintext layout
	}
	protect := backupProtector
	if protect == nil {
		protect = dpapiProtector{}
	}
	blob, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", fmt.Errorf("backup value is not valid base64: %w", err)
	}
	plain, err := protect.Unprotect(blob)
	if err != nil {
		return "", fmt.Errorf("unprotecting backup value: %w", err)
	}
	return string(plain), nil
}

// RollbackWindowsKeyNames restores every entry recorded in the backup file, in
// reverse order: recorded values are rewritten, entries that did not exist are
// deleted, and the process environment state of the clean names is restored.
//
// The backup file is untrusted input (S-9): the path may be a directory
// containing registry-keys.v2.json, the file must carry this tool's provenance
// stamp, and every entry name must pass the credential whitelist before any
// mutation happens. Machine-root writes are refused (they need elevation and
// must never be driven by file content).
func RollbackWindowsKeyNames(backupPath string, opts RepairOptions) error {
	if strings.TrimSpace(backupPath) == "" {
		return errors.New("empty backup path")
	}
	store, err := repairStore(opts.Store)
	if err != nil {
		return err
	}
	file := resolveBackupFile(backupPath)
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var backup keyBackupFile
	if err := json.Unmarshal(raw, &backup); err != nil {
		return fmt.Errorf("parsing backup %s: %w", file, err)
	}
	if backup.Tool != repairToolName {
		return fmt.Errorf("backup %s was not written by %s (tool=%q); refusing to apply", MaskOperand(file), repairToolName, MaskOperand(backup.Tool))
	}
	if len(backup.Entries) == 0 {
		return fmt.Errorf("backup %s records no entries", file)
	}

	// Validate the whole file before touching anything: a crafted backup is
	// rejected as a unit, never partially applied.
	values := make([]string, len(backup.Entries))
	for i, e := range backup.Entries {
		if err := validateBackupEntry(e); err != nil {
			return fmt.Errorf("refusing backup %s: %w", file, err)
		}
		v, err := decodeBackupValue(backup.Version, e.Value)
		if err != nil {
			// F7: the decode refusal echoes the backup-file-derived entry
			// name; mask it seeded with the raw + normalized name while the
			// %w chain to the decode error stays intact.
			name := normalizeEnvName(e.Name)
			return fmt.Errorf("%s: %w", MaskSentence(fmt.Sprintf("refusing backup %s: entry %q", file, name), e.Name, name), err)
		}
		values[i] = v
	}

	var errs []error
	for i := len(backup.Entries) - 1; i >= 0; i-- {
		e := backup.Entries[i]
		value := values[i]
		switch e.Root {
		case EnvRootProcess:
			if e.Existed {
				if err := os.Setenv(e.Name, value); err != nil {
					errs = append(errs, fmt.Errorf("process env %s: %w", MaskOperand(e.Name), err))
				}
			} else if err := os.Unsetenv(e.Name); err != nil {
				errs = append(errs, fmt.Errorf("process env %s: %w", MaskOperand(e.Name), err))
			}
		case EnvRootUser:
			if e.Existed {
				if err := store.Set(e.Root, e.Name, value); err != nil {
					errs = append(errs, fmt.Errorf("%s\\%s: %w", e.Root, MaskOperand(e.Name), err))
				}
			} else if err := store.Delete(e.Root, e.Name); err != nil {
				errs = append(errs, fmt.Errorf("%s\\%s: %w", e.Root, MaskOperand(e.Name), err))
			}
		default:
			errs = append(errs, fmt.Errorf("backup entry with unknown root %q", e.Root))
		}
	}
	return errors.Join(errs...)
}
