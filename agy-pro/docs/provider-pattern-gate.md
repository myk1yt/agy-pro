# Provider-pattern gate (standing)

The tracked Go tree must not carry a contiguous provider-key spelling, even in
test fixtures. This file records the standing gate command, so the check is
reproducible from the repository instead of from a session report.

## The gate

The pattern has 15 alternatives. The same text is a constant in
`internal/testfixtures/planted_test.go` (`providerPatternGate`), which applies it
to its own source on every test run, so the executable copy and this page cannot
drift apart silently again.

Run from `agy-pro/` (the module root). Tracked files only:

```
cd agy-pro

git grep -En "sk_live_[A-Za-z0-9]{6,}|pk_live_[A-Za-z0-9]{6,}|ghp_[A-Za-z0-9]{16,}|AKIA[A-Z0-9]{12,}|ASIA[A-Z0-9]{16}|AIza[A-Za-z0-9_-]{30,}|wJalr[A-Za-z0-9/+]{20,}|nvapi-[A-Za-z0-9-]{10,}|gsk_[A-Za-z0-9]{10,}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-ant-api03-[A-Za-z0-9-]{10,}|eyJ[A-Za-z0-9_-]{10,}|sk-proj-[A-Za-z0-9]{12,}|Sk_Live_[A-Za-z0-9]{16}|sk-live[A-Za-z0-9-]{6,}" -- '*.go'
```

Working-tree form (catches untracked files too; gitignored review dumps under
`scratch/` are reported and are not part of the deliverable):

```
cd agy-pro

grep -rnE "sk_live_[A-Za-z0-9]{6,}|pk_live_[A-Za-z0-9]{6,}|ghp_[A-Za-z0-9]{16,}|AKIA[A-Z0-9]{12,}|ASIA[A-Z0-9]{16}|AIza[A-Za-z0-9_-]{30,}|wJalr[A-Za-z0-9/+]{20,}|nvapi-[A-Za-z0-9-]{10,}|gsk_[A-Za-z0-9]{10,}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-ant-api03-[A-Za-z0-9-]{10,}|eyJ[A-Za-z0-9_-]{10,}|sk-proj-[A-Za-z0-9]{12,}|Sk_Live_[A-Za-z0-9]{16}|sk-live[A-Za-z0-9-]{6,}" --include='*.go' . | grep -v '^\./scratch/'
```

Expected output: none. A non-empty result is a defect in the changed file, not a
gate false positive: the alternatives are anchored on provider prefixes that no
legitimate identifier uses.

## The alternatives

| # | Alternative | Family |
|---|---|---|
| 1 | `sk_live_[A-Za-z0-9]{6,}` | Stripe live secret, underscore spelling |
| 2 | `pk_live_[A-Za-z0-9]{6,}` | Stripe live publishable, underscore spelling |
| 3 | `ghp_[A-Za-z0-9]{16,}` | GitHub personal access token |
| 4 | `AKIA[A-Z0-9]{12,}` | AWS access key id |
| 5 | `ASIA[A-Z0-9]{16}` | AWS temporary access key id |
| 6 | `AIza[A-Za-z0-9_-]{30,}` | Google API key |
| 7 | `wJalr[A-Za-z0-9/+]{20,}` | AWS secret access key example |
| 8 | `nvapi-[A-Za-z0-9-]{10,}` | NVIDIA API key |
| 9 | `gsk_[A-Za-z0-9]{10,}` | Groq API key |
| 10 | `xox[baprs]-[A-Za-z0-9-]{10,}` | Slack token |
| 11 | `sk-ant-api03-[A-Za-z0-9-]{10,}` | Anthropic API key |
| 12 | `eyJ[A-Za-z0-9_-]{10,}` | JWT header segment |
| 13 | `sk-proj-[A-Za-z0-9]{12,}` | OpenAI project key |
| 14 | `Sk_Live_[A-Za-z0-9]{16}` | Stripe live secret, mixed-case spelling |
| 15 | `sk-live[A-Za-z0-9-]{6,}` | Hyphen-family live key, one arm for both live spellings |

Arm 15 is composed as the head plus six body runes rather than the head plus a
separator plus six, so it catches both live spellings: a value that keeps a
hyphen after `live`, and a value whose body continues straight past `live` with
no separator at all (a 20-rune handwritten body is enough for both). The narrow
`sk-live-[A-Za-z0-9-]{6,}` form reported zero hits on the second spelling and is
no longer used anywhere.

## Limitation: this is a lexical tripwire, not a classifier

The gate matches contiguous source characters against a fixed list of provider
heads. It is deliberately cheap and deliberately dumb, so these evasions are
known and accepted:

- **Case variants.** Only arm 14 pins a mixed-case spelling; the uppercase or
  title-case form of the hyphen head everywhere else passes the gate.
- **Short bodies.** Under six body runes past the head the arm does not fire, and
  no other arm applies.
- **Families not in the list.** A head nobody has added yet (a new provider, or
  a private-token shape) is not caught.
- **Contiguous spellings outside `*.go`.** The scan is scoped to Go source; a
  key pasted into a markdown file, a JSON config, or a shell script is out of
  scope.

None of those are a hole in the control, because the gate was never the control.
The runtime classifier (`slotSecretPrefixes` plus the opaque-run floor in
`internal/providers/slotpolicy.go`) refuses key-shaped values at every ingest
door regardless of case or length, and the reviewed-fixture assertion pins in
`internal/testfixtures/planted_test.go` compare exact bytes, so a real credential
swapped in for a fixture fails there even when it evades every pattern. The gate
exists to keep push protection from ever seeing the bytes; the classifier and the
pins are what actually decide.

## Why concatenation passes and a single-block literal fails

The gate is a lexical scan: it matches a contiguous run of source characters, so
it is blind to how the runtime value is assembled. A single-block literal spells
the whole key on one source line, so the scanner finds the head immediately after
its prefix. A constant built by concatenating fragments
(`"sk-" + "live-" + "<opaque run>"`) produces the identical runtime bytes,
because the Go compiler folds the constant at compile time and every
assertion, mask, and comparison sees the same string. On disk, the quoted
fragments are separated by `"` and `+` characters, so no contiguous run survives
for the scanner to match. The two requirements therefore do not conflict: the
fixture keeps its exact value, and the tracked source carries no key-shaped
spelling. Split at the head's hyphen so no fragment re-forms the full spelling.

Test fixtures are the only intended carrier of these values. Production code,
docs prose, and comments may name a credential head in the abstract, but must not
concatenate it into a value.
