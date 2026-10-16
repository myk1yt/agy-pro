// Package agysettings owns the managed settings.json regime for the hybrid
// profiles of the native-coexistence design
// (docs/261014_0006_session_native-coexistence-design/NATIVE_COEXISTENCE_DESIGN.md §3).
//
// Profile P2 runs agy in gemini mode against the local mesh. That requires
// settings.json to carry modelProvider:"gemini" plus a managed customModels
// map. Writing the user's GLOBAL settings file is forbidden (a protected path)
// and hazardous (it would switch every session). The proven mechanism is
// --app_data_dir <relative>, which relocates the CLI app-data dir to
// <home>/.gemini/<relative> INCLUDING settings.json (design fact V6), so
// per-session settings regimes coexist under one USERPROFILE with the global
// file untouched. This package owns that regime: path resolution, a
// managed-key ledger covering every key it writes, atomic write plus
// timestamped backup plus lockfile, and byte-identical rollback.
//
// Sandbox rule. The only sanctioned sandbox is the USERPROFILE/HOME override:
// every path here resolves through os.UserHomeDir(), which honors USERPROFILE
// on Windows and HOME elsewhere. The GEMINI_CONFIG_DIR claim is deleted
// everywhere and does not appear in this package; a test pins that. Tests
// override both variables and never read or stat the real profile.
//
// Protected-path rule. This package never chooses a write target on the
// user's behalf: the caller hands Open an explicit path. GlobalSettingsPath
// exists so callers can resolve and inspect the global file; the P2 regime
// writes AppDataDirSettingsPath. Nothing here reads, writes, moves, or deletes
// anything under the real home unless the caller passes such a path
// explicitly, and no test does.
//
// Managed-key ledger. Every settings key this package can write is declared in
// ManagedKeys(). Get, Set, and Delete accept ledger keys only; a key outside
// the ledger is refused with ErrNotManaged rather than touched. Keys already
// present in the file that this package does not manage are preserved
// verbatim, and the write path fails closed if the ledger and the document
// struct ever disagree (see assertLedgerComplete).
//
// No deletion. Backups accumulate (<path>.bak.YYYYMMDD-HHMMSS, mode 0600) and a
// rollback never deletes anything: it restores the last backup byte-for-byte.
// The lockfile is a sibling of the target (<path>.lock) and is released by
// truncation, never by unlink. Every path a write cycle touches is a
// string-suffixed sibling of the target, so no traversal outside the target's
// directory is possible.
package agysettings
