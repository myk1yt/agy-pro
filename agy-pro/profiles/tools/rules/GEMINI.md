# Antigravity & Gemini Core Engineering Guidelines

These rules define standard software development, code architecture, cross-platform portability, and context hygiene for all Antigravity and Gemini CLI agents and subagents.

---

## 1. Zero External Dependency Standard
- Whenever possible in core tooling, CLI scripts, and utility libraries, rely **purely on Node.js built-in modules** (`fs`, `path`, `readline`, `os`, `crypto`, `util`, `events`, `child_process`, `http`, `https`, `url`, `stream`, etc.).
- Avoid bloatware packages (`chalk`, `commander`, `yargs`, `glob`, `rimraf`, `dotenv`) by utilizing zero-dependency ANSI helpers, recursive standard library methods (`fs.rmSync(..., { recursive: true })`), and custom lightweight CLI argument parsers.
- Keep binaries lightweight, lightning-fast (<10ms startup), and universally runnable without requiring `npm install`.

---

## 2. Multi-Platform & OS Portability
- Maintain seamless support for **Windows (CMD / PowerShell)**, **macOS (Zsh / Bash)**, and **Linux (Bash / POSIX)**.
- **Path Handling**:
  - Always use `path.join()`, `path.resolve()`, and `path.normalize()`. Never hardcode OS-specific directory separators (`/` or `\\`).
  - Resolve user home directories dynamically via `os.homedir()` (or `%USERPROFILE%` on Windows, `$HOME` on Unix).
  - Never hardcode personal username paths (e.g. `C:\\Users\\username`).
- **Terminal & Display**:
  - Check `process.stdout.isTTY` before emitting rich ANSI color sequences or terminal clear escape codes.
  - Implement ASCII fallback tables when Unicode box-drawing characters may not render cleanly on legacy terminals.
  - Support `NO_COLOR` and `CI` environment variables gracefully.

---

## 3. Modular Architecture & Clean Code Principles
- **Separation of Concerns (SoC)**: Separate business logic, file parsers, cache management, formatting/display layers, and CLI entrypoints into discrete modules.
- **Single Responsibility Principle (SRP)**: Each file and class should have one well-defined responsibility.
- **Immutability & Pure Functions**: Keep data transformations pure and decoupled from I/O side effects.
- **English-Only Comments**: Write all internal source code comments, JSDoc docstrings, type definitions, and commit messages in English.

---

## 4. Precision Token & Context Hygiene
- Avoid monolithic context dumping. Read only the necessary file slices using range parameters.
- Optimize prompt structures for **Prompt Caching** (place static guidelines, schemas, and invariants at the top; dynamic session data at the bottom).
- When logging, caching, or parsing conversation histories, utilize incremental streaming readers (`readline`, chunked file streams) to prevent out-of-memory errors on massive logfiles.

---

## 5. Robust Error Handling & Atomic Operations
- Always wrap disk file modifications in safe write routines (e.g. write to temporary file, then atomic rename `fs.renameSync`) to prevent corrupted cache states during abrupt interruptions.
- Provide human-friendly, localized error diagnostics with actionable remediation steps rather than unhandled stack traces.
- Enforce strict exit codes (0 = Success, 1 = Error / General Failure, 2 = Missing Arguments / Config Error).

---

## 6. Multi-Language Internationalization (i18n)
- Support seamless multi-language interfaces across English, Korean (`ko`), Japanese (`ja`), Simplified Chinese (`zh`), etc.
- Detect locale automatically in order of precedence:
  1. CLI parameter (e.g., `--lang ko`)
  2. Environment variables (`LANG`, `LC_ALL`, `LC_MESSAGES`)
  3. System Intl API (`Intl.DateTimeFormat().resolvedOptions().locale`)
  4. Default fallback (`en`)
- Maintain a structured dictionary mapping with variable interpolation (`{0}`, `{1}`, etc.).

---

## 7. Self-Contained Testing & Verification
- Author all unit and integration tests using Node.js built-in `assert` module and a standalone test runner in `test/run-tests.js`.
- Test suites must run cleanly via `node test/run-tests.js` with zero external testing framework dependencies.
- Ensure 100% test pass rate across all modules before cutting a release or committing to production branches.
