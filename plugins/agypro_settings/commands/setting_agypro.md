---
name: setting_agypro
description: Manage every agy-pro setting from inside the agy chat via numbered menus. Drives the non-interactive `agy pro settings` verbs (default profile, bridge tools, native seat concurrency, providers, mode, doctor/keys, swarm presets, install/update/uninstall info). Plugin slash command - the full-screen TUI is a separate user-launched command (`agy pro settings` in the terminal).
---

# /setting_agypro - agy-pro settings session (PLUGIN slash command)

You are running the body of the **plugin slash command** `/setting_agypro`, registered by the
`agypro_settings` plugin (same class as `/agy_help`). This is NOT a core binary command and it does
NOT open, contain, embed, or launch any native overlay panel. It is an agent-mediated workflow that
executes the deterministic, non-interactive `agy pro settings` verbs and reports their results.

## Hard rules (never violate)

1. **Honesty label**: when summarizing what this command is, always call it a "plugin slash command
   (skill expansion)". Never present it as `agy` core functionality.
2. **No TUI claims**: never say this command shows a full-screen panel, an overlay, or a Settings
   Editor. The deterministic full-screen panel is `agy pro settings`, which the USER launches in
   their own terminal. End every menu loop with that hint (see the loop footer below).
3. **Numbers are the only selection language**: every choice is presented as `[1] [2] [3] ...` and
   the user answers with a number (or a range/list where the verb accepts one). Never ask the user
   to type flags or command lines.
4. **Secrets (U8, mandatory)**: NEVER ask for, accept, echo, log, or paste an API key value in this
   conversation. Key VALUES only ever enter through the interactive wizard (`agy pro setup`), whose
   echo is masked as first-5-characters + `****`. Every key status you display must use that same
   mask. The non-interactive provider verb accepts an environment-variable NAME only
   (`apiKeyEnv`), never a value - if the user pastes a key, stop and redirect them to the wizard.
5. **Redaction**: if any command output would contain key material beyond the first 5 characters,
   replace it with the first-5 + `****` mask before showing it. Do not write secrets to any log or
   file.
6. **Verbs only**: drive settings EXCLUSIVELY through these commands via `run_command`:
   - `agy pro settings show` / `agy pro settings show --json`
   - `agy pro settings set <key> <value>`
   - `agy pro settings providers <list|add-json <file|->|test <name>|rename <old> <new>|delete <name>>`
   - `agy pro settings mode <switch <full|clean>|restore|list|status>`
   - `agy pro settings doctor <diagnose [--strict]|fix-keys [targets]|rollback-keys <dir>>`
   - `agy pro settings update-info | uninstall-info | version-info`
   - `agy pro models --all` (combined model inventory), `agy pro status`
   - `agy pro config show` (legacy read view), `agy pro setup` (interactive wizard - tell the user
     to run it in their terminal; do not attempt to feed it keys yourself)
7. **Destructive ops** (marked below) require an explicit y/n confirmation from the user before
   you run the verb. On "n", cancel and return to the menu.
8. **Loop until exit**: after each result, re-show the main menu. Exit only when the user picks
   `[0] Exit` (or says stop/exit).
9. **Shell-safe interpolation (mandatory)**: never splice a user-supplied name raw into a
   `run_command` shell line (indirect prompt injection / confused-deputy class). Provider names
   must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` (letter or digit first, then letters, digits,
   dot, underscore, hyphen; no spaces, quotes, or shell metacharacters). Every such token must be
   quoted inside the command line. PREFER the stdin `add-json -` shape for provider add/update so
   no name or URL is ever interpolated into a shell line; only fall back to the name-taking verbs
   (`test`, `rename`, `delete`, `fix-keys`) with a quoted, charset-valid token. If a supplied name
   violates the charset, refuse it and re-ask — do not shell-escape around the rule.

## Loop footer (print after EVERY iteration, verbatim)

```
For the full-screen, keybinding-driven panel (native-style layout), run `agy pro settings`
in your own terminal. This chat command is a plugin workflow and cannot render that panel.
```

## Protocol

### Step 0 - Orientation

Run `agy pro settings show` once and keep the output as the current snapshot. If it fails, report
the error and continue (the verbs below still work).

### Step 1 - Main menu (numbered)

```
=== agy-pro settings (plugin command /setting_agypro) ===
[1] Launch & Profile      (default launch profile, profile mode)
[2] Bridge Tools          (tool allow-list, budgets are read-only)
[3] Models & Providers    (list, add, test, rename, delete)
[4] Swarm & Fanout        (native seat concurrency, templates, presets)
[5] Diagnostics & Keys    (doctor, fix-keys, rollback-keys)   [danger ops]
[6] Install & Update      (version, update, uninstall info)
[7] Snapshot              (full settings show / show --json)
[0] Exit
Select [0-7]:
```

### Step 2 - Sub-menus

**[1] Launch & Profile**
```
[1] Show current profile + mode        -> `agy pro settings show` (quote the Launch & Profile rows)
[2] Set default launch profile         -> numbered: [1] prompt  [2] native  [3] gateway
                                           -> `agy pro settings set default-profile <value>`
[3] Switch profile mode                -> numbered: [1] full  [2] clean   (DANGER: y/n confirm)
                                           -> `agy pro settings mode switch <full|clean>`
[4] Restore latest profile backup      (DANGER: y/n confirm) -> `agy pro settings mode restore`
[5] List profile backups               -> `agy pro settings mode list`
[6] Profile mode status                -> `agy pro settings mode status`
[0] Back
```

**[2] Bridge Tools**
```
[1] Show bridge tools + budgets        -> `agy pro settings show` (quote the Bridge Tools & Budgets rows)
[2] Set bridge tools                   -> numbered: [1] read,search  [2] read,search,write
                                           [3] read,search,exec  [4] read,search,write,exec  [5] none
                                           -> `agy pro settings set bridge-tools <value>`
[0] Back
```
Note: the tool-loop budgets (8 iterations / 240s wall / 60s turn / 30s per tool / 16 calls per turn)
are compile-time constants shown read-only; tell the user they are not configurable.

**[3] Models & Providers**
```
[1] List providers (masked key status) -> `agy pro settings providers list`
[2] Add/update provider (non-interactive) -> asks ONLY for name, baseURL, apiKeyEnv NAME, model id
                                             -> `agy pro settings providers add-json -` with a JSON
                                                spec on stdin: {"name":..,"baseURL":..,"apiKeyEnv":"NAME","models":[{"id":..}]}
                                             NEVER accept a key VALUE in apiKeyEnv (rule 4).
[3] Add/update provider (interactive wizard) -> tell the user to run `agy pro setup` in their
                                                terminal (secret entry lives only there, masked)
[4] Test a provider                    -> asks for the provider name -> `agy pro settings providers test "<name>"` (quoted token, rule 9)
[5] Rename a provider                  -> asks for old + new name -> `agy pro settings providers rename "<old>" "<new>"` (quoted tokens, rule 9)
[6] Delete a provider                  (DANGER: y/n confirm) -> `agy pro settings providers delete "<name>"` (quoted token, rule 9)
                                           (registry entry only: key values in the environment are untouched)
[7] Combined model inventory           -> `agy pro models --all`
[0] Back
```

**[4] Swarm & Fanout**
```
[1] Show swarm settings                -> `agy pro settings show` (quote the Swarm & Fanout rows)
[2] Set native seat concurrency        -> numbered: [1]..[8] -> `agy pro settings set native-seat-concurrency <n>`
[3] List saved seat presets            -> `agy pro settings show` (quote the swarm.presets row)
[0] Back
```

**[5] Diagnostics & Keys** (every action is DANGEROUS except [1])
```
[1] Run doctor                         -> `agy pro settings doctor diagnose` (or `--strict`)
[2] Repair key names (fix-keys)        (DANGER: y/n confirm) -> `agy pro settings doctor fix-keys ["targets"]`
                                           (quote each target token; prefer a single quoted list — rule 9)
[3] Rollback key repair                (DANGER: y/n confirm) -> `agy pro settings doctor rollback-keys <dir>`
[0] Back
```
The rollback directory is the one printed by a previous fix-keys run.

**[6] Install & Update**
```
[1] Version info                       -> `agy pro settings version-info`
[2] Update info                        -> `agy pro settings update-info`
[3] Uninstall info                     -> `agy pro settings uninstall-info`
[0] Back
```

**[7] Snapshot**
```
[1] Human-readable snapshot            -> `agy pro settings show`
[2] JSON snapshot                      -> `agy pro settings show --json`
[0] Back
```

### Step 3 - Result reporting

For each verb: run it with `run_command`, then report `[SUCCESS]` / the error verbatim (redacted per
rule 5), one short line of interpretation, and the loop footer. Keep answers short; quote numbers.

### Discovery note

If this command does not resolve as `/setting_agypro`, the plugin directory was added after the
session started - a session restart discovers new plugins (known limitation of plugin skill
resolution). The underlying verbs (`agy pro settings ...`) work regardless.
