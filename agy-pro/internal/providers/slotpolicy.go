package providers

import (
	"net/url"
	"strings"
	"unicode"
)

// Registry-slot value policy.
//
// This is the ONE policy that decides (a) whether a registry free-text value
// carries credential material (the ingest door of the TUI model form and of the
// CLI model verbs) and (b) how a registry-slot operand is rendered at every
// display sink (the TUI approval frames and model screens, the CLI consent
// transcript, the status banner, the model inventory dump). Callers route
// through SecretShaped and MaskSlot; no caller keeps its own marker list.
//
// The four preceding rounds each grew an ENUMERATED marker list in two places
// (internal/tui/screen_model_form.go and cmd/agy/pro_settings.go), each round
// closed one sub-shape, and the two lists drifted apart, so every round left
// the same class open on one side. This file replaces both lists with a policy
// derived from Unicode categories: a newly found look-alike needs no new entry
// because it is already a member of its class.
//
// The separator class is CATEGORY-based, not enumerated: Po (other
// punctuation), Pd (dash punctuation), Pc (connector punctuation), the
// NON-ASCII members of Zs (the space separators: NBSP, U+3000 IDEOGRAPHIC
// SPACE, and the rest of the class), plus the slash-like members of Sm (math)
// and So (other symbols) named in slotSlashLikeSymbols. The ASCII space is
// deliberately NOT a member: its ASCII twin already splits a run by design, so
// admitting it would only widen the documented visible-split acceptance, and
// the ASCII-parity trade is recorded. Letters, digits, and every symbol that is
// not slash-like stay outside the class, so an unfamiliar category member is
// left unfolded rather than guessed at.
//
// The INVISIBLE class (Cc, Cf, Mn, Me: control runes, bidi overrides and
// isolates, zero-width spaces and joiners, variation selectors and combining
// marks, the BOM) is deleted before anything is classified or stripped: an
// invisible cannot carry identity, and both older twins already deleted it. The
// URL HOST LABEL is read with one wider strip, the invisible class PLUS the
// separator-space classes (Zs, Zl, Zp): a space separator renders as a blank,
// not as a separator the fold could rewrite, so the host arm deletes it instead
// (see stripHostLabelInvisibles).
//
// The FOLD (foldSeparators) normalizes only the NON-ASCII members of the
// separator class to a single ASCII "/". The ASCII members are deliberately
// left alone: they are already visible and the shape test already treats them
// as run-breaking separators, so rewriting them to "/" would join identifier
// segments into one run-admitted blob and refuse the pinned benign corpus (the
// 39-rune meta-llama id, a benign URL, a Windows path). See foldSeparators.
//
// The STRIP (stripSlotMarkers) removes, to a fixpoint: leading separator runs
// (the relative-path markers "./", "../", ".\", "..\" are member runs of this
// class, not a separate list), a leading URL scheme, a Windows drive prefix,
// and the leading separator runs behind either of them.
//
// The CLASSIFIER (SecretShaped) is the shape test both twins already shared
// (the credential prefix list, else one opaque base64 run of 16+ runes that
// carries both letter and digit classes), applied to the raw value AND to its
// fully stripped core.

// slotOpaqueRunFloorRunes is the opaque-run floor: one uninterrupted run of
// the base64 alphabet at this length is credential material (AKIA-class
// access-key ids, AWS secret keys), never a name. It is the same floor both
// earlier guards carried (internal/tui credentialTokenShaped, cmd/agy
// modelSpecOpaqueToken) and the floor of the generic contiguous-run rule in
// operandLooksSecret.
const slotOpaqueRunFloorRunes = 16

// slotSecretPrefixes is the ONE credential-head list: a value starting with
// any of these is key material in any slot, at any length. It is shared by the
// operand classifier (operandLooksSecret) and by the registry-slot policy
// (SecretShaped), so the display side and both ingest doors cannot disagree on
// a prefix again. The previous split left the display side without the
// docker-pat and bearer heads (a "dckr_pat-<token>" or "bearer <token>" value
// rendered raw wherever MaskOperand was the last masker); the union is kept
// here and the alignment is pinned.
var slotSecretPrefixes = []string{
	"sk-", "key-", "gsk_", "nvapi-", "live_", "secret_",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
	"xox", "glpat-", "npm_", "dckr_pat-", "dckr_pat_", "bearer ", "eyj",
}

// slotSlashLikeSymbols names the slash-like members of the Sm (math) and So
// (other symbols) categories: the two categories hold separator look-alikes
// next to symbols that are not separator-like at all (plus, equals, less-than,
// tilde), so the members are named here instead of admitting the whole
// category. Everything not listed is left unfolded, per the conservative rule.
// The solidus-with-overbar pair (U+29F6, U+29F7) joined the list after the
// policy review showed the two render as a slash and a backslash while staying
// outside every category rule the fold reads.
var slotSlashLikeSymbols = map[rune]bool{
	'\u2044': true, // FRACTION SLASH (Sm)
	'\u2215': true, // DIVISION SLASH (Sm)
	'\u2216': true, // SET MINUS (Sm, renders as a backslash)
	'\u27CB': true, // MATHEMATICAL RISING DIAGONAL (Sm)
	'\u27CC': true, // LONG DIVISION (Sm)
	'\u29F5': true, // REVERSE SOLIDUS OPERATOR (Sm)
	'\u29F6': true, // SOLIDUS WITH OVERBAR (Sm, renders as a slash)
	'\u29F7': true, // REVERSE SOLIDUS WITH HORIZONTAL STROKE (Sm, renders as a backslash)
	'\u29F8': true, // BIG SOLIDUS (Sm)
	'\u29F9': true, // BIG REVERSE SOLIDUS (Sm)
	'\u2AFD': true, // DOUBLE SOLIDUS OPERATOR (Sm)
	'\u2571': true, // BOX DRAWINGS LIGHT DIAGONAL UPPER RIGHT TO LOWER LEFT (So)
	'\u2572': true, // BOX DRAWINGS LIGHT DIAGONAL UPPER LEFT TO LOWER RIGHT (So)
	'\u2573': true, // BOX DRAWINGS LIGHT DIAGONAL CROSS (So)
}

// isSlotSeparatorRune reports whether r belongs to the policy's separator
// class. This predicate, and not a list of runes, decides what a leading
// marker is, so a look-alike that renders as a dot, a dash, a slash, a space,
// or a connector is a marker the moment Unicode files it in one of the classes.
// The space class is admitted only ABOVE ASCII: the ASCII space stays outside
// the class so the ASCII members keep their visible-split parity (see the file
// header).
func isSlotSeparatorRune(r rune) bool {
	return unicode.Is(unicode.Po, r) || unicode.Is(unicode.Pd, r) ||
		unicode.Is(unicode.Pc, r) || (r > unicode.MaxASCII && unicode.Is(unicode.Zs, r)) ||
		slotSlashLikeSymbols[r]
}

// isSlotInvisibleRune reports whether r carries no visible identity: the
// control class (Cc), the format class (Cf: bidi overrides and isolates,
// zero-width spaces and joiners, the BOM), and the non-spacing and enclosing
// mark classes (Mn, Me: variation selectors and combining marks).
func isSlotInvisibleRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r)
}

// StripSlotControls deletes the control runes (Cc) and the format class (Cf)
// from a value before it reaches a display sink. The TUI painter strips
// control runes from untrusted frame lines at render time, but the approval
// summary and the CLI transcript are composed by their callers, and a Cf rune
// inside a credential defeats the classifier before any painter sees it. The
// TUI form and the CLI verbs both delegate here, so the rule lives once.
func StripSlotControls(v string) string {
	if v == "" {
		return v
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, v)
}

// stripSlotInvisibles deletes the invisible class (Cc, Cf, Mn, Me) from a
// value: the strip the fold applies before it rewrites separator look-alikes,
// exposed on its own for a classification that needs the VISIBLE form without
// the rewrite. foldSeparators is that caller. The URL host-label arm needs the
// same deletion PLUS the separator-space classes, so it calls
// stripHostLabelInvisibles (below), which composes this predicate instead of
// widening it.
//
// The class is the fold's, not just the display strip's Cc+Cf pair: MaskSlot
// deletes all four classes on its way to a classification, so a combining mark
// must not survive here either. The result is identical to the input for a value
// with no invisible rune.
func stripSlotInvisibles(v string) string {
	if v == "" {
		return v
	}
	return strings.Map(func(r rune) rune {
		if isSlotInvisibleRune(r) {
			return -1
		}
		return r
	}, v)
}

// isSlotHostLabelInvisibleRune reports whether r is deleted from a URL host
// LABEL before the label is classified: the invisible class (Cc, Cf, Mn, Me)
// PLUS the three separator-space classes, Zs (space separators: the ASCII space,
// NBSP U+00A0, the ideographic space U+3000, the figure and narrow no-break
// spaces), Zl (U+2028 LINE SEPARATOR) and Zp (U+2029 PARAGRAPH SEPARATOR).
//
// The space classes are named HERE and not in isSlotInvisibleRune because the
// two strips answer different questions. The fold REWRITES a non-ASCII separator
// member to "/", which is what lets an interior look-alike join the run the
// shape test reads; a host label has no fold and needs the rune DELETED, because
// a space separator renders as a blank rather than a slash while url.Parse keeps
// it in Hostname and no transport rule rejects it. Extending the shared
// predicate instead would have moved the fold's verdicts for the path, query,
// fragment, and userinfo arms (there a Zs member currently splits a word at its
// "/" rewrite), which this unit's boundary forbids. A space separator inside
// Hostname splits a credential run exactly as a zero-width space did:
// "https://sk_live_0123456789ab<U+00A0>cdef/v1" was an identity under every arm.
func isSlotHostLabelInvisibleRune(r rune) bool {
	return isSlotInvisibleRune(r) || unicode.Is(unicode.Zs, r) ||
		unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// stripHostLabelInvisibles deletes the host-label class (see
// isSlotHostLabelInvisibleRune) from a label, so the label is classified in its
// visible form. It is the identity on a label with no member rune, so every
// pre-existing host verdict is byte-identical: the space classes join the strip
// only for a label that actually carries one, and url.Parse rejects an ASCII
// space in the host, so on the URL path the wider class is reached only through
// the NBSP/U+3000/Zl/Zp members.
func stripHostLabelInvisibles(v string) string {
	if v == "" {
		return v
	}
	return strings.Map(func(r rune) rune {
		if isSlotHostLabelInvisibleRune(r) {
			return -1
		}
		return r
	}, v)
}

// isSlotURIPartLineSeparatorRune reports whether r is deleted from one URL PART
// before the part is classified: the two line-separator classes, Zl (U+2028 LINE
// SEPARATOR) and Zp (U+2029 PARAGRAPH SEPARATOR).
//
// They are named HERE, and not added to isSlotInvisibleRune or to the separator
// class, because both of those sets are pinned: the fold's deletion set is Cc,
// Cf, Mn, Me and its rewrite set is the non-ASCII separator class, so admitting
// Zl or Zp to either would move a path, query, fragment, or Opaque verdict the
// fix8-7 boundary owns (TestBaseURLHostLabelSpaceSeparatorStrip asserts both
// sets separately). A Zl/Zp member is neither invisible to the fold nor a slash
// look-alike: nothing deletes it, nothing rewrites it, and the run scan
// therefore FLUSHES at it.
//
// The host label already carries the same two members inside its own wider
// strip. This is that arm's part-side twin, kept separate for the reason above:
// a host label has no fold, a part does, and the fold's Zs rewrite ("to a
// slash") is the part-side answer for the space class.
func isSlotURIPartLineSeparatorRune(r rune) bool {
	return unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// stripURIPartLineSeparators deletes the part-arm class (see
// isSlotURIPartLineSeparatorRune) from one URL part, so the part is classified in
// the form a transport carries it. Zl and Zp are not legitimate characters of a
// path segment, a query key or value, a fragment, a userinfo operand, or the
// Opaque slot; url.Parse keeps them inside the part and no transport rule
// rejects them, so they were the one decoration left after the host arm closed
// its own (fix8-7). The measured shape is
// "<head>_<12-rune run><U+2028><14-rune run>": neither half reaches the 16-rune
// floor, nothing deletes the separator, so the run scan flushed in the middle and
// the part was the IDENTITY of MaskSlot at every URL door while the display side
// (which shares the classifier) echoed it raw. Deleting the member joins the
// halves exactly as the undecorated twin is joined.
//
// The strip is the identity on a part with no member rune, so every pre-existing
// part verdict is byte-identical: no pinned corpus row carries a Zl/Zp in a part
// slot (the fix8-7 rows carry theirs in the host label, which this arm never
// reads).
func stripURIPartLineSeparators(v string) string {
	if v == "" {
		return v
	}
	return strings.Map(func(r rune) rune {
		if isSlotURIPartLineSeparatorRune(r) {
			return -1
		}
		return r
	}, v)
}

// foldSeparators deletes the invisible runes (Cc, Cf, Mn, Me) and rewrites
// every NON-ASCII separator-class rune to a single ASCII "/", so a decoration
// outside ASCII reduces exactly like its ASCII twin: a leading look-alike is a
// leading separator run, and an interior look-alike separator joins the run
// the shape test sees ("ab3def7<U+FF0F>gh9jk1mnpqr5t" is one 20-rune run, the
// same as the ASCII form).
//
// The ASCII members of the class are NOT rewritten. They are already visible
// and the shape test already breaks a run at them, which is what keeps the
// pinned benign corpus benign: folding "-" and "." to "/" (both are class
// members) would turn "meta-llama/Llama-3.3-70B-Instruct-Turbo" and
// "https://api.example.com/v1/completions" into one run-admitted blob each and
// refuse them. Rewriting only the members the predicates cannot already read
// keeps the class closed without over-refusing the accept side; the excluded
// ASCII members are every ASCII Po/Pd/Pc rune (".", "-", "_", ":", "/", "\",
// ",", ";", "!", "?", "'", "(", ")", "[", "]", "{", "}", "@", "#", "%", "&",
// "*" and the quote runes), the ASCII space (a Zs member the class admits only
// above ASCII), and the ASCII members of Sm ("+", "=", "<", ">", "|", "~"),
// which are not slash-like, so they are not part of the class at all.
//
// The space class is folded only ABOVE ASCII, so an NBSP or an ideographic
// space inside a credential joins the run exactly as an ASCII space's absence
// would ("ab3def7<U+00A0>gh9jk1mnpqr5t" is one 21-rune run, the same as the
// undecorated core), while a benign single-space composite ("beta fast") and
// every ASCII-separated id keep their current accept verdict, because the
// fold never touches the ASCII space.
func foldSeparators(v string) string {
	v = stripSlotInvisibles(v)
	if v == "" {
		return v
	}
	return strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII && isSlotSeparatorRune(r) {
			return '/'
		}
		return r
	}, v)
}

// stripSlotMarkers removes the leading decoration a registry slot never
// legitimately carries, returning the value's opaque core: leading separator
// runs (this is where "./", "../", ".\", "..\", a leading "/" or "\", and a
// leading run of look-alike separators all reduce), a leading URL scheme
// ("https://", "file:///"), a Windows drive prefix ("C:\", "C:/", "C:"), and
// the leading separator runs behind either of them. The passes repeat until
// the value stops changing.
//
// Termination: every pass that changes the value deletes at least one rune
// (a leading separator run deletes one or more, the scheme and drive passes
// delete a letter plus at least one separator), and no pass ever inserts a
// rune, so the rune count strictly decreases on every non-final iteration and
// the loop halts after at most len(runes) iterations. The result is
// idempotent: a second call returns the same core, because the value it
// returns has no leading marker left to strip.
//
// On the folded form the drive check is "letter followed by a separator run"
// rather than the ASCII "letter + ':' + separator": the ASCII ':' is already a
// class member, and the fold rewrites the colon look-alikes (U+FF1A, U+FE10) to
// the same separator rune, so a folded "C:/<core>" and a folded
// "Z" + U+FF1A + U+FF0F + "<core>" both reduce. The rule is classification-only
// and can only shorten a value, so it cannot turn a benign value secret; a
// single letter ahead of a separator is a drive prefix in every registry slot.
func stripSlotMarkers(v string) string {
	r := []rune(v)
	for {
		before := len(r)
		for len(r) > 0 && isSlotSeparatorRune(r[0]) {
			r = r[1:]
		}
		if n := slotSchemeEnd(r); n > 0 {
			r = r[n:]
		}
		if n := slotDriveEnd(r); n > 0 {
			r = r[n:]
		}
		for len(r) > 0 && isSlotSeparatorRune(r[0]) {
			r = r[1:]
		}
		if len(r) == before {
			return string(r)
		}
	}
}

// slotSchemeEnd returns the index just past a LEADING URL scheme (a letter-led
// run followed by two or more separator-class runes: "://" is three class
// members, and the folded look-alike forms are two or more), or 0 when the
// value does not start with one. Only a leading scheme counts: a value that
// merely CONTAINS a URL names no location and must render intact.
func slotSchemeEnd(r []rune) int {
	if len(r) == 0 || !unicode.IsLetter(r[0]) {
		return 0
	}
	i := 1
	for i < len(r) {
		c := r[i]
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '+' || c == '-' || c == '.' {
			i++
			continue
		}
		break
	}
	n := 0
	for i+n < len(r) && isSlotSeparatorRune(r[i+n]) {
		n++
	}
	if n < 2 {
		return 0
	}
	return i + n
}

// slotDriveEnd returns the index just past a LEADING Windows drive prefix (one
// letter followed by at least one separator-class rune), or 0 when the value
// does not start with one. See stripSlotMarkers for why the check does not
// require a literal ':' (the fold rewrites the colon look-alikes).
func slotDriveEnd(r []rune) int {
	if len(r) < 2 || !unicode.IsLetter(r[0]) || !isSlotSeparatorRune(r[1]) {
		return 0
	}
	i := 1
	for i < len(r) && isSlotSeparatorRune(r[i]) {
		i++
	}
	return i
}

// slotShapeLooksSecret is the shape test proper, byte-identical to the one
// both twins carried (internal/tui credentialTokenShaped, cmd/agy
// modelSpecOpaqueToken) plus the shared prefix list: a known credential head
// at any length, else one uninterrupted run of the base64 alphabet at the
// 16-rune floor carrying BOTH letter and digit classes. The mixed-class gate is
// what keeps a long single-class word ("abcdefghijklmnopqrstuvwxyz"), a
// digit-only serial, and a hyphenated or dotted id readable; the "/" member of
// the run alphabet is what catches a separator-carrying AWS secret.
func slotShapeLooksSecret(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	for _, p := range slotSecretPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return slotOpaqueRunShaped(s)
}

// slotOpaqueRunShaped reports whether s is one uninterrupted base64-alphabet
// run at the floor with both letter and digit classes present.
func slotOpaqueRunShaped(s string) bool {
	n := 0
	letter, digit := false, false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			letter = true
		case r >= '0' && r <= '9':
			digit = true
		case r == '+', r == '/', r == '=':
		default:
			return false
		}
		n++
	}
	return n >= slotOpaqueRunFloorRunes && letter && digit
}

// slotSegmentShaped reports whether any base64-alphabet segment of a folded
// core is a credential run at the floor. It is the DISPLAY arm for composed
// labels: the status banner and the model inventory render "<id> (<provider>)"
// as one string, so classifying the whole string never sees the credential
// (the annotation breaks the run) and classifying only the id leaves the core
// readable inside the label. The ingest classifier stays whole-value: a composed
// label is never a model field, so this arm is display-only and can only mask.
func slotSegmentShaped(core string) bool {
	return slotRunScan(core, slotJoinSlashAlphabet)
}

// slotRewriteShaped is the arm for a value the registry-slot authority REWROTE,
// and it reads fragments rather than words.
//
// The authority strips a leading scheme, drive, or relative marker before its
// own classifiers run, so a benign location comes back as a bare path
// ("https://api.example.com/v1/completions" becomes
// "api.example.com/v1/completions"). Read with the segment arm, the last three
// path components join over their slashes into an 18-rune run and a pinned
// benign URL masks; the composed labels this arm exists for are word-bounded by
// construction ("<id> (<provider>)", "thinking(<effort>)", "tools,
// thinking(<effort>)").
//
// The word-bounded read left the two-arm hole open. The identity arm measures a
// core with the base64 alphabet ("+", "/", "=" join the run) while a word read
// breaks at every one of them, so a decorated core that reaches the floor only
// ACROSS those separators ("<marker>/abcd1234+efgh5678 (lab)",
// "abcd1234+efgh5678 (lab)") was masked by the identity arm and rendered raw by
// this one, at the status banner id column and the models --json DisplayName
// field. This scan closes it fragment-wise:
//   - "+" and "=" join unconditionally. Neither is a location character, and no
//     pinned benign rewrite joins over them.
//   - "/" joins only when the rewrite is NOT the bare core of a URL or a path
//     (slotRewriteIsLocationStrip). Joining over the slashes of a location
//     rewrite masks the pinned benign URL, and on that path every "/" is a path
//     separator, not a base64 rune.
//
// The join can only mask, never accept: a value this arm masks is already
// refused at every ingest door in its undecorated twin, and a value the
// authority masked re-classifies as benign here (MaskSecret of a mask is not a
// run), so nothing is masked twice.
func slotRewriteShaped(core, original string) bool {
	if slotRewriteIsLocationStrip(original) {
		return slotRunScan(core, slotJoinPlusEquals)
	}
	return slotSegmentShaped(core)
}

// slotRewriteIsLocationStrip reports whether the registry-slot authority's
// rewrite of original is the bare core of a URL or a path: the value carried a
// leading URL scheme, a Windows drive prefix, a relative-path marker (./ ../ .\
// ..\), or a leading separator run, so stripLocationMarkers deleted the
// decoration and every "/" left in the rewrite is a PATH separator rather than a
// base64 rune. It asks the authority's own stripper, so the two cannot disagree
// about what a location rewrite is. Only such a rewrite turns the slash join off
// (see slotRewriteShaped).
func slotRewriteIsLocationStrip(original string) bool {
	t := strings.TrimSpace(original)
	return t != "" && stripLocationMarkers(t) != t
}

// slotJoinSlashAlphabet and slotJoinPlusEquals are the two run-join predicates
// the scans above read. The slash alphabet is the base64 one, so a credential
// carrying "/" ("wJalrXUtnFEMI/K7MDENG/...") stays one run; the plus-equals
// alphabet is the rewrite arm's, which joins the two non-location members and
// leaves the path separator to the caller's decision.
func slotJoinSlashAlphabet(r rune) bool { return r == '+' || r == '/' || r == '=' }

func slotJoinPlusEquals(r rune) bool { return r == '+' || r == '=' }

// slotRunScan is the shared scan: one pass over the value, flushing the current
// run at every rune the join predicate declines (a nil predicate joins nothing,
// which is the plain word read). A flushed run masks when it reaches the floor
// with both letter and digit classes.
func slotRunScan(core string, join func(rune) bool) bool {
	n := 0
	letter, digit := false, false
	flush := func() bool {
		shaped := n >= slotOpaqueRunFloorRunes && letter && digit
		n, letter, digit = 0, false, false
		return shaped
	}
	for _, r := range core {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			letter = true
			n++
		case r >= '0' && r <= '9':
			digit = true
			n++
		case join != nil && join(r):
			n++
		default:
			if flush() {
				return true
			}
		}
	}
	return flush()
}

// SlotSecretPrefixes returns a copy of the shared credential-head list. It
// exists so a caller that scans several values against the same list (the TUI
// prompt editor scans a multi-line buffer line by line) reads the ONE policy
// list instead of keeping a copy that can drift; the copy keeps the policy's
// slice immutable for callers.
func SlotSecretPrefixes() []string {
	out := make([]string, len(slotSecretPrefixes))
	copy(out, slotSecretPrefixes)
	return out
}

// OpaqueRunShaped reports whether s is one uninterrupted base64-alphabet run
// at the shared floor carrying both letter and digit classes: the policy's
// shape test without the prefix list, for callers that already checked heads
// separately (the TUI prompt editor) or that need the run test alone.
func OpaqueRunShaped(s string) bool { return slotOpaqueRunShaped(s) }

// SecretShaped reports whether a registry free-text value carries credential
// material rather than a name. It classifies the raw value first and then the
// FULLY STRIPPED core (fold + strip), so one decoration of any class cannot
// blind the shape test: a leading marker of any separator-class rune, a
// Windows drive (bare or nested), a URL scheme, an invisible rune ahead of or
// inside the credential, and an interior look-alike separator all reduce to the
// same core. A value whose core equals the value is classified exactly once, so
// every pre-existing accept verdict is unchanged. Nothing is ever rewritten:
// the caller persists the value verbatim.
func SecretShaped(v string) bool {
	t := strings.TrimSpace(v)
	if t == "" {
		return false
	}
	if slotShapeLooksSecret(t) {
		return true
	}
	core := stripSlotMarkers(foldSeparators(t))
	return core != "" && core != t && slotShapeLooksSecret(core)
}

// NameCarriesCredential reports whether v is credential material rather than a
// provider name or endpoint value. It is the one ingest authority for the
// underscore-head key class that SecretShaped and MaskOperand both miss: the
// union of the three arms the TUI form proved necessary.
//
// Each arm answers a different shape, and the union is deliberately not an
// enumerated prefix list (a list refused the legal names "key_backup" and
// "sk_staging" and reopens on the next unlisted head):
//   - SecretShaped is the shared policy classifier (prefix heads plus one
//     opaque base64 run at the 16-rune floor, read raw and after the fold and
//     strip of a decorated value);
//   - MaskOperand is the CLI operand display authority, which additionally
//     catches the decorated-secret and JWT display classes;
//   - MaskSlot is the registry-slot display authority, whose segment arm reads
//     a value the other two classify whole. That arm is the only one that
//     refuses the underscore-head class: "sk_live_0123456789abcdef",
//     "tok_a1b2c3d4e5f6g7h8i9j0", "pk_live_...", "key_...", and "AKIA_..."
//     carry no head in slotSecretPrefixes and no uninterrupted run (the "_"
//     breaks it), so the first two arms are the identity on all five while the
//     16+ alphanumeric segment behind the separator masks.
//
// The union is exactly the pre-fix name-arm verdict set plus those five probes,
// which the slotpolicy suite pins as a live before/after table, so a legal
// record is never newly refused by a door that adopts this authority. A door
// that ingests a NAME (the TUI provider form, the setup wizard, the model-ID
// and CLI-twin doors) calls this function instead of keeping its own union.
func NameCarriesCredential(v string) bool {
	return SecretShaped(v) || MaskOperand(v) != v || MaskSlot(v) != v
}

// BaseURLCarriesCredential reports whether a base URL carries credential
// material in any of its slots: the host label, userinfo username or password,
// path segments (plus the Opaque form), query keys and values, and the
// fragment. It is the one URL ingest authority; the display maskers are
// unchanged.
//
// It is the ONE rule the three base-URL ingest doors call (the TUI provider
// form's field arm, the setup wizard's two URL prompts, and the add-json
// parser), replacing the per-door copies that drifted: the form classified the
// value whole plus the path, query, fragment, and userinfo; the wizard
// classified the same slots with its own identifier helpers; the CLI add-json
// door classified nothing. A door that ingests an endpoint calls this function
// and keeps no local part list.
//
// The arms, in order:
//   - the WHOLE value, exactly as the wizard door had it: SecretShaped (the
//     shared policy classifier) or MaskOperand changing the value. This is the
//     arm that refuses a bare key pasted into the URL slot even when
//     url.Parse calls the value hostless, a decorated secret, and a JWT, and it
//     is also what keeps every pre-existing whole-value refusal (a 16-rune
//     opaque run behind the scheme, an AWS key id, a JWT-shaped spelling)
//     exactly where it was;
//   - the HOST labels, through baseURLHostCarriesCredential. This is the slot
//     the phase-4 review swarm found unclassified at both doors:
//     "https://sk_live_0123456789abcdef/v1" passes the whole-value arms (its
//     "_" breaks every run) and resolved as a DNS name, leaking the key to the
//     resolver. The rule is targeted (see that function) so a long gateway
//     host label stays accepted;
//   - the userinfo username AND password. A benign userinfo operand stays
//     accepted: the transport rule accepts userinfo, and the wizard already
//     persisted such a URL, so a blanket "userinfo means credential" arm (the
//     form's old MaskBaseURL comparison) over-refused a legal shape;
//   - every path segment, plus the Opaque slot of a hostless "https:<key>"
//     spelling (the transport rule accepts a scheme-only value, and url.Parse
//     reports the key as Opaque with an empty Path);
//   - every query key and value, both sides of a pair ("?<key>=1" puts the
//     credential in the key slot);
//   - the fragment.
//
// Each part is classified by baseURLPartCarriesCredential, the shared per-part
// rule (MaskSlot changes the part), with the identifier exemption the form
// landed. Classification only: nothing is rewritten.
//
// A value url.Parse cannot read is REFUSED. The fall-through that used to
// return false here deferred to the transport rule on the theory that the
// transport rule redacts its own echo; the fix8 review measured that theory
// false. NewRedactor's patterns are the identity on the underscore-head class
// (skRe needs a hyphen, and "sk_live_<16>" carries an underscore), so the
// operator's parse error body carried the raw key into the wizard refusal, the
// loader, the mutation op, and the TUI message line. A malformed value is not
// an endpoint, so failing closed costs an ingest door nothing; the trade is
// that a merely malformed URL (a stray "%" in a path, an invalid port) is
// refused with the credential sentence instead of a syntax diagnosis. The
// transport rule keeps its own value-free refusal (validateBaseURL) for the
// registry read-back path, which no ingest door guards.
//
// The query slot is read PAIR BY PAIR from the RAW query string, not through
// url.Query(): Query() silently drops every pair whose percent-decoding fails,
// so "?api_key=sk_live_0123456789abcdef%2" was invisible to every arm and was
// accepted at all three doors. Each raw pair is decoded when its own escape
// sequence is legal (a well-formed pair classifies exactly as before) and
// classified in its RAW form when it is not (a malformed pair is still seen).
//
// The path and Opaque slots are read in BOTH forms. url.Parse decodes the path
// slot once, but a hostless "https:<value>" spelling keeps the hostless
// remainder in u.Opaque VERBATIM: "https:...%e2%80%a8..." reaches the arm as the
// escape TEXT, so the percent bytes break the run at every escape and a
// credential whose post-escape tail falls under the floor reads as location
// material. Each segment is classified raw (the pre-existing verdict) and, when
// its own escapes decode legally, ALSO in decoded form. The decode is
// url.PathUnescape, the path-and-Opaque semantics: neither slot is
// form-encoded, so a literal "+" stays the base64 member it is instead of
// becoming a space (QueryUnescape's rule, which the query arm keeps because a
// query IS form-encoded). A segment whose escapes do not decode legally is
// classified raw only, so a malformed-but-benign segment keeps its verdict. The
// decoded read sits beside the raw read and can only add a refusal.
//
// The HOST is read in both forms for the same reason. url.Parse decodes a
// non-ASCII host escape itself ("%e2%80%a8" arrives as U+2028), but a "%25"
// escape becomes the literal "%" it decoded to, so "https://%2573%256b..."
// reaches the label rule as the escape TEXT "%73%6b...", where every percent
// byte breaks the run the rule reads. The host labels are classified raw (the
// pre-existing verdict) and, when the host's own escapes decode legally, ALSO
// through the same label rule after a url.PathUnescape of the host. It is the
// same decode choice the path arm makes: a host is not form-encoded, so a
// literal "+" stays the base64 rune it is. A host whose escapes are malformed
// stays raw-only, and a host with no escape decodes to itself, so every benign
// host keeps its verdict.
func BaseURLCarriesCredential(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if SecretShaped(trimmed) || MaskOperand(trimmed) != trimmed {
		return true
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return true
	}
	host := u.Hostname()
	if baseURLHostCarriesCredential(host) {
		return true
	}
	// See the doc comment: a "%25" host escape reaches this arm as escape TEXT,
	// so classify the decoded host too when its own escapes are legal. A host
	// whose escapes are malformed stays raw-only; url.PathUnescape keeps a
	// literal "+" as the base64 rune it is (a host is not form-encoded).
	if decoded, err := url.PathUnescape(host); err == nil && decoded != host && baseURLHostCarriesCredential(decoded) {
		return true
	}
	if u.User != nil {
		if baseURLPartCarriesCredential(u.User.Username()) {
			return true
		}
		if pw, ok := u.User.Password(); ok && baseURLPartCarriesCredential(pw) {
			return true
		}
	}
	for _, slot := range []string{u.Path, u.Opaque} {
		for _, seg := range strings.Split(slot, "/") {
			if baseURLPartCarriesCredential(seg) {
				return true
			}
			// See the doc comment: the Opaque slot arrives undecoded, and a path
			// segment can carry a second escape layer, so classify the decoded
			// form too when its own escapes are legal. A segment whose escapes are
			// malformed stays raw-only, exactly as before.
			if decoded, err := url.PathUnescape(seg); err == nil && decoded != seg && baseURLPartCarriesCredential(decoded) {
				return true
			}
		}
	}
	if baseURLQueryCarriesCredential(u.RawQuery) {
		return true
	}
	return baseURLPartCarriesCredential(strings.TrimSpace(u.Fragment))
}

// baseURLQueryCarriesCredential classifies a RAW query string pair by pair. It
// exists because url.URL.Query() unwraps the decode error and drops every pair
// it cannot decode: a credential in a malformed pair
// ("?api_key=sk_live_0123456789abcdef%2") reached no arm at all, so the value
// was accepted at all three doors and persisted verbatim. Splitting on "&" and
// then on the first "=" is the split ParseQuery performs, and decoding each side
// independently keeps every well-formed verdict byte-identical: a side that
// decodes is classified decoded (what Query() already did), a side that does not
// is classified raw (what Query() could never do). Both sides are read, because
// "?<key>=1" puts the credential in the key slot.
//
// One tightening rides along: ParseQuery also drops any pair carrying a bare
// ";" (RFC 3986 removed it as a separator), so "?a=sk_live_0123456789abcdef;x=1"
// was invisible too. It is classified now, and a credential-shaped value there
// is refused.
func baseURLQueryCarriesCredential(rawQuery string) bool {
	if rawQuery == "" {
		return false
	}
	for _, pair := range strings.Split(rawQuery, "&") {
		if pair == "" {
			continue
		}
		rawKey, rawValue, _ := strings.Cut(pair, "=")
		key, keyErr := url.QueryUnescape(rawKey)
		if keyErr != nil {
			key = rawKey
		}
		value, valueErr := url.QueryUnescape(rawValue)
		if valueErr != nil {
			value = rawValue
		}
		if baseURLPartCarriesCredential(key) || baseURLPartCarriesCredential(value) {
			return true
		}
	}
	return false
}

// slotHostKeyIDHeads is the host-label head class beyond the shared credential
// prefix list: the AWS access-key-id heads. The value policy catches that shape
// through the opaque-run arm, which the host rule deliberately leaves to the
// whole-value arms (a host label at the run floor cannot be told apart from a
// long gateway name), so the two documented access-key-id heads are named here
// and required to be followed by a run at the same floor. Naming them keeps
// "asia.example.com" (a region-shaped host) accepted while
// "ASIAIOSFODNN7EXAMPLE" is refused.
var slotHostKeyIDHeads = []string{"akia", "asia"}

// baseURLHostCarriesCredential reports whether a URL host carries credential
// material in any dot-separated LABEL. The host slot was the last unclassified
// door of the class: a key pasted as the host ("https://sk_live_.../v1") passed
// every whole-value arm (the "_" breaks the run) and every part arm (the host
// is not a part), was persisted raw, echoed raw, and was resolved as a DNS name
// by the live probe.
func baseURLHostCarriesCredential(host string) bool {
	if host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if baseURLHostLabelCarriesCredential(label) {
			return true
		}
	}
	return false
}

// baseURLHostLabelCarriesCredential is the targeted host-label test. It refuses
// the three shapes the review seats named, and nothing else:
//   - a HEAD: the label starts with a shared credential prefix ("ghp_...",
//     "gsk_...", "nvapi-...", the JWT header "eyJ...") or with an AWS
//     access-key-id head followed by a run at the floor;
//   - a DECORATED label: the label is not one uninterrupted base64-alphabet
//     run and the display authority masks it (the decorated-secret class
//     "1-ASIAIOSFODNN7EXAMPLE", a hyphen-carrying AWS secret, a redactor
//     pattern head);
//   - a credential RUN BEHIND A SHORT HEAD: a 16+ mixed-class run behind one or
//     more short alphabetic tokens ("sk_live_0123456789abcdef",
//     "tok_a1b2c3d4e5f6g7h8i9j0", "key_0123456789abcdef"), the underscore-head
//     class that has no shared prefix and no uninterrupted run.
//
// A bare opaque run is deliberately NOT refused by this function: it is the
// whole-value arms' business, and they already decide it
// ("https://0123456789abcdef/v1" is refused there, because the stripped core
// joins the host and the path into one run, while "mycompany2024gateway" and
// "acmeprod2024gateway" stay accepted). Refusing every long run here would
// refuse every location-shaped gateway host, which is the false dichotomy the
// review rejected. A UUID form and a lowercase-hex identifier are exempt like
// every other URL part.
//
// The label is read in its VISIBLE form: the host-label class is stripped
// before anything is tested. Every other slot already receives a strip (MaskSlot
// first stage), which is why the host was the one slot an invisible rune could
// blind: "https://sk_live_0123456789ab<U+200B>cdef/v1" parses, keeps the rune in
// Hostname, passes every whole-value arm, and the run test then fails on the
// split run. The strip is the fold's four classes (Cc, Cf, Mn, Me) PLUS the three
// separator-space classes (Zs, Zl, Zp: see stripHostLabelInvisibles), so a
// combining mark cannot work and neither can a blank: a run split by an NBSP,
// U+2028, or U+2029 blinded this arm the same way the zero-width space did. A
// label with no member rune is byte-identical to its input, so every benign host
// keeps its verdict.
func baseURLHostLabelCarriesCredential(label string) bool {
	label = stripHostLabelInvisibles(label)
	if label == "" || baseURLPartIsIdentifier(label) {
		return false
	}
	lower := strings.ToLower(label)
	for _, head := range slotSecretPrefixes {
		if strings.HasPrefix(lower, head) {
			return true
		}
	}
	for _, head := range slotHostKeyIDHeads {
		if strings.HasPrefix(lower, head) && slotOpaqueRunShaped(label[len(head):]) {
			return true
		}
	}
	if slotBase64Run(label) {
		// One uninterrupted run: not this function's shape (see above).
		return false
	}
	if MaskOperand(label) != label {
		return true
	}
	return baseURLHostLabelRunBehindShortHead(label)
}

// slotHostHeadTokenMaxRunes is the length ceiling of a decoration head token. A
// real credential decoration is a short word ("sk", "pk", "rk", "tok", "key",
// "live"), so a long readable compound ("api-gateway-...") is not a
// decoration; the shared prefix list already names every head that is long
// enough to matter.
const slotHostHeadTokenMaxRunes = 5

// baseURLHostLabelRunBehindShortHead reports whether a host label is a
// credential run wearing a short decoration: a 16+ mixed-class base64 run at
// index 1 or later of the label's "-"/"_" token sequence, with every preceding
// token a short alphabetic head. It is the one arm that reads the
// underscore-head class in the host slot, and the head ceiling is what keeps
// "aci-gw-2024-production" and "us-east-1" accepted.
func baseURLHostLabelRunBehindShortHead(label string) bool {
	tokens := strings.FieldsFunc(label, func(r rune) bool { return r == '-' || r == '_' })
	if len(tokens) < 2 {
		return false
	}
	for i := 1; i < len(tokens); i++ {
		if !slotOpaqueRunShaped(tokens[i]) || baseURLPartIsIdentifier(tokens[i]) {
			continue
		}
		head := true
		for _, prev := range tokens[:i] {
			if !slotShortAlphaHead(prev) {
				head = false
				break
			}
		}
		if head {
			return true
		}
	}
	return false
}

// slotShortAlphaHead reports whether one token can be a credential decoration
// head: alphabetic and no longer than slotHostHeadTokenMaxRunes.
func slotShortAlphaHead(t string) bool {
	if t == "" || len([]rune(t)) > slotHostHeadTokenMaxRunes {
		return false
	}
	for _, r := range t {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// slotBase64Run reports whether s is one uninterrupted run of the base64
// alphabet, the "merely a long opaque run" shape a host label is allowed to be.
func slotBase64Run(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '+', r == '/', r == '=':
		default:
			return false
		}
	}
	return true
}

// baseURLPartCarriesCredential reports whether one URL part (a path segment, a
// query key or value, a fragment, a userinfo operand, a host label) is
// credential material. The transport layer splits a URL into parts, so the
// whole-value classifiers can never see one; the registry-slot masker reads
// each part exactly as a registry slot.
//
// The part is classified in its VISIBLE form: the Zl/Zp classes are deleted
// first (stripURIPartLineSeparators). A line or paragraph separator survives the
// fold and flushes the run scan, so a credential split across one read as two
// short words and was accepted at every door; the host label closed its own
// share of that class in fix8-7, and this is the part-side twin.
//
// Ordinary parts ("v1", "chat", "completions", "api", "v2", "hello",
// "2024-02-01") are identity under it, so a benign URL keeps its exact accept
// verdict. The pure IDENTIFIER shape is exempt (see baseURLPartIsIdentifier):
// the registry-slot masker reads one uninterrupted 16+ rune base64 run as an
// opaque credential, which is right for a key and wrong for a gateway URL that
// carries an account id. The Cloudflare AI gateway endpoint
// "https://gateway.ai.cloudflare.com/v1/<32 lowercase hex>/anthropic" was
// therefore refused before the exemption, and that endpoint is a legitimate
// base URL. A known head ("sk-", "nvapi-"), a decorated form, a JWT, and a
// mixed base64 run outside the hex alphabet all still classify as credential
// material, because none of them can satisfy a pure identifier shape.
func baseURLPartCarriesCredential(part string) bool {
	if part == "" {
		return false
	}
	part = stripURIPartLineSeparators(part)
	if part == "" {
		return false
	}
	if baseURLPartIsIdentifier(part) {
		return false
	}
	return MaskSlot(part) != part
}

// baseURLIdentifierHexFloor is the length at which an all-lowercase-hex URL part
// is an identifier rather than an opaque credential run. It is 20, not the
// shared 16-rune opaque-run floor, for two pinned reasons: the FIX-L pin requires
// a bare 16-rune mixed base64 path segment (".../v1/0123456789abcdef") to stay
// REFUSED, and a 16-rune hex string is exactly that, so the identifier class has
// to start above the floor the credential class already owns. Every real
// identifier length the class exists for sits above it (24 = MongoDB ObjectId,
// 32 = MD5 digest and Cloudflare account id, 40 = SHA-1, 64 = SHA-256).
const baseURLIdentifierHexFloor = 20

// baseURLPartIsIdentifier reports whether a URL part is a pure identifier shape:
// an all-lowercase-hex run at the identifier floor, or the UUID form
// (8-4-4-4-12 hex). Both shapes are mixed-class runs that the slot masker reads
// as opaque credential material, and both are ordinary location identifiers:
// gateway account ids are 32 hex, and resource ids, request ids, and session
// ids are UUIDs. A UUID is already identity under the masker today (its hyphen
// groups stay under the run floor), so naming it here keeps the exemption
// explicit rather than incidental.
func baseURLPartIsIdentifier(part string) bool {
	return isLowerHexIdentifier(part) || isUUIDFormIdentifier(part)
}

// isLowerHexIdentifier reports whether s is one uninterrupted all-lowercase-hex
// run at the identifier floor. Uppercase hex is deliberately NOT exempt: an
// uppercase run is the base64 class's own shape, and no pinned gateway id needs
// it.
func isLowerHexIdentifier(s string) bool {
	if len(s) < baseURLIdentifierHexFloor {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// isUUIDFormIdentifier reports whether s is exactly the 8-4-4-4-12 hex UUID
// form with the four hyphens at their fixed positions. Both hex cases are
// accepted: a UUID is an identifier whichever case it was written in, and the
// slot masker is the identity on both today.
func isUUIDFormIdentifier(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// MaskSlot is the single registry-slot display authority: every display sink
// of a providers.json-borne value routes here (TUI approval frames, success
// sentences and model screens, CLI consent transcript, mutation headlines,
// success lines, re-verify refusals, status banner, model inventory dump, the
// `models --all` listing, and the swarm seat list).
//
// The pipeline, every branch mask-only or identity (it can never unmask):
//   - strip the control runes (Cc + Cf) first, so an invisible cannot ride
//     into a frame or a transcript;
//   - MaskRegistryValue next, the registry-slot authority (location-shaped
//     key-shaped values mask here rather than ride MaskOperand's location
//     exemption);
//   - classify the authority's OUTPUT before accepting a rewrite. The authority
//     changes a value in two ways, and only one of them masks: it masks, or it
//     renders the marker-stripped core of a location-shaped value ("./gemini"
//     becomes "gemini"). Its core classifiers predate this policy's category
//     fold, so an inner plant that still wears an ASCII marker ("." + U+2025 +
//     "/" + a 20-rune core), or that sits behind a composed label ("<id>
//     (<provider>)"), comes back as a CHANGED but readable core. Running the
//     shared fold + strip + fragment scan on that output is what closes the
//     shape; a rewrite the policy also declines is returned unchanged, and an
//     authority mask re-classifies as benign, so nothing is ever masked twice.
//     The fragment scan joins "+" and "=" unconditionally and "/" only off the
//     URL/path rewrite path (slotRewriteShaped): joining over a location
//     rewrite's slashes would mask the pinned benign URL;
//   - when the authority returns the value unchanged, fold + strip with the
//     shared policy and, if the value or its core reads as credential material
//     (or the core carries a credential-shaped segment, the composed-label
//     case), render MaskSecret of the core. This is the arm the CLI twin
//     lacked: a look-alike-decorated plant (U+2025 + "/" + a 20-rune core)
//     reads as a credential to both ingest doors and previously rendered raw in
//     the CLI transcript while the TUI masked it;
//   - otherwise return the control-stripped value, byte-identical, so a benign
//     slot keeps its exact rendering (the diff stays byte-stable).
func MaskSlot(v string) string {
	clean := StripSlotControls(v)
	out := MaskRegistryValue(clean)
	if out != clean {
		// The authority rewrote the value. Re-classify the form it is about to
		// return: a credential core shielded by an ASCII marker that the
		// authority strips, or one carried inside a composed label, is invisible
		// to the authority's own core classifiers but is exactly what the
		// policy's fold, strip, and fragment scan read. The fragment scan (see
		// slotRewriteShaped) joins "+" and "=" unconditionally and "/" only off
		// the URL/path rewrite path: on that path every "/" is a path separator,
		// and joining over it would mask the pinned benign URL rewrite.
		if pcore := stripSlotMarkers(foldSeparators(out)); pcore != "" &&
			(SecretShaped(out) || slotRewriteShaped(pcore, clean)) {
			return MaskSecret(pcore)
		}
		return out
	}
	core := stripSlotMarkers(foldSeparators(clean))
	if core == "" {
		core = clean
	}
	if SecretShaped(clean) || slotSegmentShaped(core) {
		return MaskSecret(core)
	}
	return clean
}
