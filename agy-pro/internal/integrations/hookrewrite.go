// Deploy-time command rewrite for the hooks.json the hook feature deploys.
//
// The portable source files (repo hooks/hooks.json and its byte-identical
// snapshot twin agy-pro/profiles/tools/hooks/hooks.json) ship the PostInvocation
// command as the portable launcher name plus its own argument tail,
// "agy-tokens --hook": FIX-13 moved the flag inside the one command string the host
// documents, so the portable form carries its arguments and not only the bare name.
// A command value that is the bare PATH name "agy-tokens" with nothing after it is
// the fallback shape, and the host CLI resolves either spelling through PATH at
// every turn, which is the untrusted-search-path class F-V1 filed
// (docs/261023_0001_session_agy-pro-session9/security-reviewer-report-r1-nwglmflex-verify.md,
// CWE-426) and the gap pick-record item 6 closed for statusLine.command: the
// deployed form pins the absolute stable-wrapper path, the bare name is the
// documented fallback.
//
// The source files stay portable on purpose. They are compared byte for byte by
// the profiles sync gate, so a machine-specific absolute path inside them would
// either break that gate or bake one user's home directory into everyone's copy.
// The rewrite therefore happens HERE, once per deploy, on the bytes the plan is
// about to write:
//
//   - THE COMMAND VALUE ONLY. Every other byte of the file is the source's own
//     byte: indentation, key order, the trailing newline and the file's line
//     endings survive because the engine splices one JSON string literal instead
//     of re-marshalling the document.
//   - THE SAME LAUNCHER AUTHORITY AS THE STATUSLINE DOOR. The path comes from
//     ResolveStatusLineLauncher and its quoting from quoteLauncher
//     (settings.go), so the two places agy-pro pins a launcher can never drift
//     apart. Nothing here re-implements the ladder.
//   - VERIFIED, NOT TRUSTED. The spliced bytes are read back and checked against
//     the source before they are used: every byte outside the rewritten literal
//     must be identical, every other command value must be unchanged, and the
//     document must still decode to the same JSON value except for the values
//     that were rewritten. A rewrite that fails any of those checks is dropped
//     and the portable bytes deploy unchanged with the reason recorded.
//   - THE FALLBACK IS ALWAYS DEPLOYABLE. A machine with no launcher yet still
//     gets a hooks.json (the portable form, exactly as it shipped) plus a diff row
//     naming the missing launcher. That posture is not silent: Status reports the
//     file as drifted once a launcher exists, and repair re-applies the pinned
//     form.
package integrations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agypro/internal/providers"
)

// hookCommandKey is the JSON key whose string value names the command a hook
// runs. It is the only field this file rewrites.
const hookCommandKey = "command"

// hookPortableCommand is the portable launcher name. A command value that starts
// with it as a whole word is the portable form, whether or not an argument tail
// follows it (the shipped source spells "agy-tokens --hook").
const hookPortableCommand = "agy-tokens"

// hookJSONMaxDepth bounds the scanner's descent. A hooks file is a handful of
// levels deep; the bound turns a pathological document into a declined rewrite
// instead of a deep stack.
const hookJSONMaxDepth = 64

// HookCommandPosture is how the deployed hooks.json names its command.
type HookCommandPosture string

const (
	// HookCommandAbsolute means the command value was rewritten at deploy time
	// to the destination machine's absolute launcher path: pick record item 6's
	// shipped form, and the fix for F-V1.
	HookCommandAbsolute HookCommandPosture = "absolute"
	// HookCommandBare means no launcher existed when the file was planned, so the
	// portable bare PATH name ships unchanged: pick record item 6's documented
	// fallback, the statusline door's degrade vocabulary reused for the hook.
	HookCommandBare HookCommandPosture = "bare"
	// HookCommandUnchanged means there was nothing this engine could pin: the
	// source names no command value, its command is not the portable launcher, or
	// the rewrite did not verify. The bytes deployed are the source bytes.
	HookCommandUnchanged HookCommandPosture = "unchanged"
)

// HookCommandRewrite records what the deploy-time rewrite did to the hook file.
// It is carried by the plan, so a renderer can tell a pinned deploy from the
// fallback without re-reading anything.
type HookCommandRewrite struct {
	// Posture is how the deployed bytes name the command.
	Posture HookCommandPosture
	// Launcher is the absolute launcher the value was rewritten to; empty for
	// every posture but absolute.
	Launcher string
	// Value is the command value the deployed bytes carry at the FIRST rewritten
	// command site, in its executable spelling (quoted when the path needs it);
	// empty when nothing was rewritten. A document with several command values
	// records its first here and counts them in Sites.
	Value string
	// Portable is the source value the first rewritten command site held,
	// arguments included; empty when nothing was rewritten.
	Portable string
	// Sites is how many command values the rewrite pinned: 0 when it declined or
	// had nothing to pin, 1 for the shipped hooks file, more for a document that
	// names the launcher at several sites.
	Sites int
	// Shape is the documented launcher location for this OS (the sentence
	// fragment a missing-launcher diagnostic names instead of a resolved path).
	Shape string
	// Candidates are the absolute launcher paths consulted, in priority order,
	// when none of them existed.
	Candidates []string
	// Detail is the reason a posture was taken, in the engine's plain English
	// chrome: the launcher diagnosis for the fallback, the verification failure
	// for a declined rewrite.
	Detail string
}

// Rewritten reports whether the deployed bytes differ from the source bytes.
func (r *HookCommandRewrite) Rewritten() bool {
	return r != nil && r.Posture == HookCommandAbsolute
}

// PostureText renders the posture as one diff sentence for the gate that shows
// the hook's deploy block. It is a fact row, not a note: a note says what the
// write will NOT do, and this write does happen. The sentence mirrors the
// statusline door's degrade vocabulary (compose.go ComposeDoorStep: "the
// agy-tokens launcher is missing (<shape>), so ..."), so the two places a
// launcherless machine is explained read the same way.
func (r *HookCommandRewrite) PostureText() string {
	if r == nil {
		return ""
	}
	switch r.Posture {
	case HookCommandAbsolute:
		text := "the deployed command is pinned to the absolute launcher " + providers.MaskOperand(r.Launcher)
		if r.Sites > 1 {
			text = fmt.Sprintf("%s (%d command values pinned)", text, r.Sites)
		}
		return text
	case HookCommandBare:
		shape := r.Shape
		if shape == "" {
			shape = "the documented launcher location"
		}
		detail := ""
		if r.Detail != "" {
			detail = " (" + r.Detail + ")"
		}
		return fmt.Sprintf("the deployed command keeps the bare PATH name: no agy-tokens launcher at %s%s; install the launcher and re-run apply to pin the absolute path",
			shape, detail)
	default:
		if r.Detail == "" {
			return ""
		}
		return "the command was not rewritten: " + providers.MaskOperand(r.Detail)
	}
}

// hookCommandRewriteFor decides the bytes the hook feature deploys and records
// the posture it took. It always returns writable bytes: a source it cannot pin
// deploys exactly as it shipped, with the reason in the posture record.
func hookCommandRewriteFor(source []byte) ([]byte, *HookCommandRewrite) {
	spans, err := hookCommandSpans(source)
	if err != nil {
		return source, &HookCommandRewrite{
			Posture: HookCommandUnchanged,
			Detail:  "the hook file could not be read as JSON (" + err.Error() + ")",
		}
	}
	portable := 0
	for _, span := range spans {
		if _, ok := hookPortableTail(span.Value); ok {
			portable++
		}
	}
	if portable == 0 {
		return source, &HookCommandRewrite{
			Posture: HookCommandUnchanged,
			Detail:  "the hook file's command is not the portable " + hookPortableCommand + " name",
		}
	}

	launcher, launcherErr := ResolveStatusLineLauncher()
	if launcherErr != nil {
		posture := &HookCommandRewrite{Posture: HookCommandBare, Portable: hookPortableCommand}
		var missing *StatusLineLauncherError
		if errors.As(launcherErr, &missing) {
			posture.Shape = missing.Shape
			posture.Candidates = missing.Candidates
			posture.Detail = missing.Detail
		} else {
			posture.Detail = launcherErr.Error()
		}
		return source, posture
	}
	return hookCommandRewriteTo(source, spans, launcher)
}

// hookCommandRewriteTo splices launcher into every portable command value of the
// document and verifies the result against the source.
func hookCommandRewriteTo(source []byte, spans []hookCommandSpan, launcher string) ([]byte, *HookCommandRewrite) {
	edits := make([]hookCommandEdit, 0, len(spans))
	for _, span := range spans {
		value, ok := hookCommandValue(span.Value, launcher)
		if !ok {
			continue
		}
		literal, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return source, &HookCommandRewrite{
				Posture: HookCommandUnchanged,
				Detail:  "the rewritten command could not be encoded as a JSON string: " + marshalErr.Error(),
			}
		}
		edits = append(edits, hookCommandEdit{span: span, value: value, literal: literal})
	}
	if len(edits) == 0 {
		return source, &HookCommandRewrite{
			Posture: HookCommandUnchanged,
			Detail:  "the hook file's command is not the portable " + hookPortableCommand + " name",
		}
	}

	rewritten := spliceHookCommands(source, edits)
	if verifyErr := verifyHookCommandRewrite(source, rewritten, spans, edits, launcher); verifyErr != nil {
		return source, &HookCommandRewrite{
			Posture: HookCommandUnchanged,
			Detail:  "the rewritten hook file did not verify against its source: " + verifyErr.Error(),
		}
	}

	return rewritten, &HookCommandRewrite{
		Posture:  HookCommandAbsolute,
		Launcher: launcher,
		Value:    edits[0].value,
		Portable: edits[0].span.Value,
		Sites:    len(edits),
	}
}

// hookLiveCommandPosture reports what the live hook file's command means on this
// machine. It answers two questions with one rule, and never writes:
//
//   - STALE, drift. The live bytes are behind what this machine calls for and
//     re-running apply would actually pin them. Status reports the destination as
//     drifted and repair re-applies, which is a real reconciliation.
//   - ADVISORY, no drift. The live bytes are the bytes this engine would deploy,
//     but there is something the operator has to know: either the rewrite declines
//     on this document (so re-running apply writes nothing), or the command points
//     at a launcher that is gone (so nothing can pin it back without unpinning it
//     first).
//
// The first bullet's "would actually pin" is the F-01 agreement rule: the stale
// decision calls the SAME function the deploy path calls, so a document the
// rewrite declines can never read as repairable drift. Its bytes are the engine's
// expected bytes by construction, and telling the operator to "re-run apply to pin
// it" would send them to a verb that writes nothing.
//
// The reverse direction is advisory by design (a CEO ruling carried by the final
// review): a pinned command whose launcher is gone stays CLEAN, because repair
// would have to unpin it back to the bare PATH name, which re-opens F-V1. The
// Detail says what to do instead.
func hookLiveCommandPosture(destination string) (string, bool) {
	data, err := os.ReadFile(destination)
	if err != nil {
		return "", false
	}
	spans, err := hookCommandSpans(data)
	if err != nil {
		// A live file this build cannot read as JSON gets no posture claim. Its
		// digest already decided the state, and guessing here would be a second
		// opinion nobody asked for.
		return "", false
	}

	if hookNamesPortableCommand(data) {
		_, posture := hookCommandRewriteFor(data)
		if posture == nil {
			return "", false
		}
		switch posture.Posture {
		case HookCommandAbsolute:
			return fmt.Sprintf("the deployed command is still the bare PATH name %q while a launcher exists at %s; re-run apply to pin it",
				hookPortableCommand, providers.MaskOperand(posture.Launcher)), true
		case HookCommandBare:
			// No launcher: the fallback IS the intended posture on this machine.
			return "", false
		default:
			reason := posture.Detail
			if reason == "" {
				reason = "the rewrite declined this document"
			}
			return fmt.Sprintf("the deployed command keeps the bare PATH name and this engine will not rewrite it (%s); re-running apply writes the same bytes, so the hook document is the thing to fix",
				providers.MaskOperand(reason)), false
		}
	}

	for _, span := range spans {
		launcherPath := hookCommandLauncherPath(span.Value)
		if launcherPath == "" {
			continue
		}
		if _, statErr := os.Stat(launcherPath); statErr == nil || !os.IsNotExist(statErr) {
			// Present, or unreadable for a reason that is not "gone": no claim.
			continue
		}
		return fmt.Sprintf("the deployed command points at %s, which is not on disk; agy-pro will not unpin it back to a PATH name, so re-run apply when the launcher is back",
			providers.MaskOperand(launcherPath)), false
	}
	return "", false
}

// hookCommandLauncherPath returns the absolute LOCAL launcher path a command value
// names, or "" when it names something else. The leading token is read through the
// door's own commandLauncher (settings.go), so the two places a launcher spelling is
// parsed agree.
//
// Two limits keep the answer to "is this the launcher THIS engine deploys?" and away
// from a claim whose cost the engine cannot pay (F-K3-1). The live hooks.json is
// user-editable, so both inputs below are attacker-chosen on a hostile machine:
//
//   - the basename must BE one of the ladder's launcher names for this host
//     (statusLineLauncherNames), not merely start with the portable name. It was a
//     prefix test, which classified lookalikes (agy-tokens-backup.bat, agy-tokensX,
//     agy-tokens-extra) as this engine's launcher and printed an advisory about a
//     file this engine never writes.
//   - every token that leads with TWO path separators, in any mixture, is rejected
//     before it can reach the os.Stat in hookLiveCommandPosture: the UNC roots
//     \\host\share\... and //host/share/..., the \\?\ / \\.\ device prefixes, and the
//     mixed roots /\host\share\..., \/host/share\... and /\\host\... (isUNCPathToken).
//     stat-ing a network path opens an SMB connection that can hand the victim's NTLM
//     challenge/response to an attacker-chosen host. A network share is never where
//     the installers put the launcher, so no true claim is lost.
func hookCommandLauncherPath(value string) string {
	token := commandLauncher(value)
	if !isAbsoluteLauncherPath(token) {
		return ""
	}
	if isUNCPathToken(token) {
		return ""
	}
	if !launcherNameOnLadder(filepath.Base(token)) {
		return ""
	}
	return token
}

// launcherNameOnLadder reports whether base is one of the file names the settings
// door's ladder resolves on this host, compared for equality against the ladder's own
// per-OS vocabulary (statusLineLauncherNames). Asking the ladder instead of restating
// the names keeps "can this engine pin it" and "is this engine's launcher" from
// drifting apart; the language is case-sensitive because the value the engine itself
// writes is the ladder's spelling, and a differently-cased live value is not this
// engine's pin in the first place.
func launcherNameOnLadder(base string) bool {
	for _, name := range statusLineLauncherNames(runtimeGOOS) {
		if base == name {
			return true
		}
	}
	return false
}

// isUNCPathToken reports whether an absolute token names a network location instead of
// a local file. Every spelling that leads with TWO path separators is refused, in any
// mixture: the UNC roots \\host\share\... and //host/share/..., the Windows device
// prefixes \\?\ and \\.\, and the mixed roots /\host\share\..., \/host/share/... and
// /\\host\... that Windows normalises to the same UNC path. Testing only the \\ and //
// prefixes let the mixed roots through, so such a token still reached the os.Stat in
// hookLiveCommandPosture (FIX-14). Every refused spelling is absolute, so
// isAbsoluteLauncherPath accepts it, and none is a path this engine writes; a
// drive-letter path (C:\...) and a POSIX path with ONE leading separator (/home/...)
// carry at most one, so neither is refused here.
func isUNCPathToken(token string) bool {
	if len(token) < 2 {
		return false
	}
	return isLauncherPathSeparator(token[0]) && isLauncherPathSeparator(token[1])
}

// isLauncherPathSeparator reports whether b separates path components on either host.
// Both spellings class as separators on both platforms because the command value may
// have been produced elsewhere, which is the rule isAbsoluteLauncherPath already follows.
func isLauncherPathSeparator(b byte) bool {
	return b == '/' || b == '\\'
}

// hookNamesPortableCommand reports whether a hooks.json document still names the
// bare launcher in any command value.
func hookNamesPortableCommand(data []byte) bool {
	spans, err := hookCommandSpans(data)
	if err != nil {
		return false
	}
	for _, span := range spans {
		if _, ok := hookPortableTail(span.Value); ok {
			return true
		}
	}
	return false
}

// hookPortableTail reports whether a command value names the portable launcher
// and returns the argument text that follows it, byte for byte. The portable name
// must be a whole word: "agy-tokens --hook" is portable, "agy-tokens-extra" and
// "./agy-tokens" are not (they are somebody else's command, not this launcher).
func hookPortableTail(value string) (string, bool) {
	if !strings.HasPrefix(value, hookPortableCommand) {
		return "", false
	}
	tail := value[len(hookPortableCommand):]
	if tail != "" && tail[0] != ' ' && tail[0] != '\t' {
		return "", false
	}
	return tail, true
}

// hookCommandValue renders the deployed form of a portable command value: the
// quoted absolute launcher followed by the original argument text, so a file that
// spells "agy-tokens --hook" keeps its own arguments.
//
// The quoting is quoteLauncher's (settings.go), and it is CORRECT for this slot: the
// Antigravity host runs a hook handler's command as a SHELL command line, not as an
// argv[0] spawn. Verified against the host itself on this machine (agy.exe carries
// its own hooks reference text): "**`command`** (string, required): The shell command
// to execute (run via `sh -c` on Unix, `cmd /c` on Windows). `~` is expanded to the
// home directory." The handler fields it documents are type, command and timeout; the
// separate "args" array the portable source files used to carry (dropped in FIX-13, when
// the flag moved inline) is not one of them. So a spaced launcher has to be quoted
// exactly as the statusLine slot quotes it, and the two slots keep sharing one spelling
// rule (probe E of the final review, SH-2/F-C1).
func hookCommandValue(value, launcher string) (string, bool) {
	tail, ok := hookPortableTail(value)
	if !ok || launcher == "" {
		return "", false
	}
	return quoteLauncher(launcher) + tail, true
}

// hookCommandSpan is one JSON string literal that is the value of a "command"
// key: the byte range of the literal (Start is the opening quote, End is just
// after the closing quote) and its decoded value.
type hookCommandSpan struct {
	Start int
	End   int
	Value string
}

// hookCommandEdit is one verified replacement: the span it replaces and the
// literal it puts there.
type hookCommandEdit struct {
	span    hookCommandSpan
	value   string
	literal []byte
}

// spliceHookCommands returns source with every edit's literal substituted for its
// span. Everything else is copied verbatim, which is what keeps the file's
// indentation, key order, line endings and trailing newline intact.
func spliceHookCommands(source []byte, edits []hookCommandEdit) []byte {
	size := len(source)
	for _, edit := range edits {
		size += len(edit.literal) - (edit.span.End - edit.span.Start)
	}
	out := make([]byte, 0, size)
	cursor := 0
	for _, edit := range edits {
		out = append(out, source[cursor:edit.span.Start]...)
		out = append(out, edit.literal...)
		cursor = edit.span.End
	}
	return append(out, source[cursor:]...)
}

// verifyHookCommandRewrite proves the spliced bytes are the source's bytes except
// for the rewritten command literals. It re-reads the result instead of trusting
// the splice:
//
//  1. the result still parses as JSON and carries the same number of command
//     values, in the same order;
//  2. every byte outside a command literal is identical (walked span by span, in
//     both directions, including the tail after the last command);
//  3. a rewritten value reads back as the value this engine computed, and every
//     value it did NOT rewrite is byte-identical;
//  4. the decoded document differs from the source's in exactly one value per
//     rewritten literal, and nowhere else.
func verifyHookCommandRewrite(source, rewritten []byte, spans []hookCommandSpan, edits []hookCommandEdit, launcher string) error {
	outSpans, err := hookCommandSpans(rewritten)
	if err != nil {
		return fmt.Errorf("the rewritten file does not read back as JSON: %v", err)
	}
	if len(outSpans) != len(spans) {
		return fmt.Errorf("the rewritten file carries %d command value(s); the source carries %d", len(outSpans), len(spans))
	}
	editAt := make(map[int]hookCommandEdit, len(edits))
	for _, edit := range edits {
		editAt[edit.span.Start] = edit
	}

	sourceCursor, rewrittenCursor := 0, 0
	for index, span := range spans {
		outSpan := outSpans[index]
		if !bytes.Equal(source[sourceCursor:span.Start], rewritten[rewrittenCursor:outSpan.Start]) {
			return fmt.Errorf("bytes before command value %d changed; only the value itself may be rewritten", index+1)
		}
		edit, rewrittenHere := editAt[span.Start]
		switch {
		case rewrittenHere && outSpan.Value != edit.value:
			return fmt.Errorf("command value %d reads %q after the rewrite; want %q", index+1, outSpan.Value, edit.value)
		case !rewrittenHere && outSpan.Value != span.Value:
			return fmt.Errorf("command value %d changed although it is not the portable launcher: %q -> %q", index+1, span.Value, outSpan.Value)
		}
		sourceCursor, rewrittenCursor = span.End, outSpan.End
	}
	if !bytes.Equal(source[sourceCursor:], rewritten[rewrittenCursor:]) {
		return errors.New("the bytes after the last command value changed")
	}

	var before, after any
	if err := json.Unmarshal(source, &before); err != nil {
		return fmt.Errorf("the source does not decode: %v", err)
	}
	if err := json.Unmarshal(rewritten, &after); err != nil {
		return fmt.Errorf("the rewritten file does not decode: %v", err)
	}
	differences, diffErr := hookRewriteDifferences(before, after, launcher)
	if diffErr != nil {
		return diffErr
	}
	if differences != len(edits) {
		return fmt.Errorf("%d JSON value(s) differ from the source; the rewrite accounts for %d", differences, len(edits))
	}
	return nil
}

// hookRewriteDifferences walks two decoded JSON values in parallel and counts the
// string differences that are exactly a portable-launcher rewrite. Any other
// difference - a type change, a key that appeared or vanished, an array of a
// different length, a different string - is an error, so this is the "equal
// except that one field" check in code rather than in a comment.
func hookRewriteDifferences(source, rewritten any, launcher string) (int, error) {
	switch src := source.(type) {
	case map[string]any:
		dst, ok := rewritten.(map[string]any)
		if !ok {
			return 0, errors.New("an object became something other than an object")
		}
		if len(dst) != len(src) {
			return 0, fmt.Errorf("an object has %d key(s) after the rewrite; the source had %d", len(dst), len(src))
		}
		total := 0
		for key, srcValue := range src {
			dstValue, present := dst[key]
			if !present {
				return 0, fmt.Errorf("the key %q disappeared from the rewritten file", key)
			}
			added, err := hookRewriteDifferences(srcValue, dstValue, launcher)
			if err != nil {
				return 0, err
			}
			total += added
		}
		return total, nil
	case []any:
		dst, ok := rewritten.([]any)
		if !ok {
			return 0, errors.New("an array became something other than an array")
		}
		if len(dst) != len(src) {
			return 0, fmt.Errorf("an array holds %d element(s) after the rewrite; the source held %d", len(dst), len(src))
		}
		total := 0
		for index := range src {
			added, err := hookRewriteDifferences(src[index], dst[index], launcher)
			if err != nil {
				return 0, err
			}
			total += added
		}
		return total, nil
	case string:
		dst, ok := rewritten.(string)
		if !ok {
			return 0, errors.New("a string became something other than a string")
		}
		if src == dst {
			return 0, nil
		}
		if pinned, portable := hookCommandValue(src, launcher); portable && pinned == dst {
			return 1, nil
		}
		return 0, fmt.Errorf("a string value changed outside the command rewrite: %q -> %q", src, dst)
	default:
		if fmt.Sprint(source) != fmt.Sprint(rewritten) {
			return 0, fmt.Errorf("a value changed outside the command rewrite: %v -> %v", source, rewritten)
		}
		return 0, nil
	}
}

// hookCommandSpans locates every "command" string value in a hooks.json document
// without changing a byte. The document must be valid JSON first: the walk below
// is a structural read, and a document that does not parse has no command value
// this engine can pin.
func hookCommandSpans(data []byte) ([]hookCommandSpan, error) {
	if !json.Valid(data) {
		return nil, errors.New("the document is not valid JSON")
	}
	scanner := &hookJSONScanner{data: data}
	if err := scanner.value(0); err != nil {
		return nil, err
	}
	scanner.skipSpace()
	if scanner.pos != len(scanner.data) {
		return nil, fmt.Errorf("unexpected bytes at offset %d", scanner.pos)
	}
	return scanner.spans, nil
}

// hookJSONScanner is a minimal structural walk over valid JSON bytes. It records
// the byte range of every string literal that is the value of a "command" key and
// ignores everything else, so a string that merely LOOKS like a command (a
// description, an argument, a type field spelled "command") can never be mistaken
// for one.
type hookJSONScanner struct {
	data  []byte
	pos   int
	spans []hookCommandSpan
}

// skipSpace advances past JSON whitespace.
func (s *hookJSONScanner) skipSpace() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\r', '\n':
			s.pos++
		default:
			return
		}
	}
}

// value reads one JSON value. Nested objects and arrays recurse, bounded by
// hookJSONMaxDepth.
func (s *hookJSONScanner) value(depth int) error {
	if depth > hookJSONMaxDepth {
		return fmt.Errorf("the document nests deeper than %d levels", hookJSONMaxDepth)
	}
	s.skipSpace()
	if s.pos >= len(s.data) {
		return errors.New("the document ends where a value was expected")
	}
	switch s.data[s.pos] {
	case '{':
		return s.object(depth + 1)
	case '[':
		return s.array(depth + 1)
	case '"':
		_, _, _, err := s.stringLiteral()
		return err
	}
	return s.scalar()
}

// object reads one JSON object, recording a span for every "command" key whose
// value is a string.
func (s *hookJSONScanner) object(depth int) error {
	s.pos++ // '{'
	s.skipSpace()
	if s.pos < len(s.data) && s.data[s.pos] == '}' {
		s.pos++
		return nil
	}
	for {
		s.skipSpace()
		if s.pos >= len(s.data) || s.data[s.pos] != '"' {
			return fmt.Errorf("expected an object key at offset %d", s.pos)
		}
		_, _, key, err := s.stringLiteral()
		if err != nil {
			return err
		}
		s.skipSpace()
		if s.pos >= len(s.data) || s.data[s.pos] != ':' {
			return fmt.Errorf("expected ':' after the key %q at offset %d", key, s.pos)
		}
		s.pos++
		s.skipSpace()
		if key == hookCommandKey && s.pos < len(s.data) && s.data[s.pos] == '"' {
			start, end, value, valueErr := s.stringLiteral()
			if valueErr != nil {
				return valueErr
			}
			s.spans = append(s.spans, hookCommandSpan{Start: start, End: end, Value: value})
		} else if err := s.value(depth); err != nil {
			return err
		}
		s.skipSpace()
		switch {
		case s.pos < len(s.data) && s.data[s.pos] == ',':
			s.pos++
		case s.pos < len(s.data) && s.data[s.pos] == '}':
			s.pos++
			return nil
		default:
			return fmt.Errorf("expected ',' or '}' at offset %d", s.pos)
		}
	}
}

// array reads one JSON array.
func (s *hookJSONScanner) array(depth int) error {
	s.pos++ // '['
	s.skipSpace()
	if s.pos < len(s.data) && s.data[s.pos] == ']' {
		s.pos++
		return nil
	}
	for {
		if err := s.value(depth); err != nil {
			return err
		}
		s.skipSpace()
		switch {
		case s.pos < len(s.data) && s.data[s.pos] == ',':
			s.pos++
		case s.pos < len(s.data) && s.data[s.pos] == ']':
			s.pos++
			return nil
		default:
			return fmt.Errorf("expected ',' or ']' at offset %d", s.pos)
		}
	}
}

// scalar reads one number, true, false or null. The document is already known to
// be valid JSON, so the token only has to be skipped, not validated.
func (s *hookJSONScanner) scalar() error {
	start := s.pos
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\r', '\n', ',', '}', ']':
			if s.pos == start {
				return fmt.Errorf("empty value at offset %d", start)
			}
			return nil
		}
		s.pos++
	}
	if s.pos == start {
		return fmt.Errorf("empty value at offset %d", start)
	}
	return nil
}

// stringLiteral reads one JSON string literal at the current position. It returns
// the byte range of the literal (quotes included) and its decoded value, so the
// caller can splice the range without re-encoding the escapes around it.
func (s *hookJSONScanner) stringLiteral() (start, end int, decoded string, err error) {
	start = s.pos
	index := start + 1
	for index < len(s.data) {
		switch s.data[index] {
		case '\\':
			index += 2
			continue
		case '"':
			if unmarshalErr := json.Unmarshal(s.data[start:index+1], &decoded); unmarshalErr != nil {
				return 0, 0, "", fmt.Errorf("string literal at offset %d: %v", start, unmarshalErr)
			}
			s.pos = index + 1
			return start, index + 1, decoded, nil
		}
		index++
	}
	return 0, 0, "", fmt.Errorf("unterminated string literal at offset %d", start)
}
