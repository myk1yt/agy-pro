package main

import (
	"fmt"
	"strings"
	"unicode"

	"agypro/internal/integrations"
)

// Terminal neutralisation for config-borne transcript lines (FIX-12 / F-S9F-1).
//
// providers.MaskOperand is the secret-masking authority, never a control-character
// boundary: a value it deliberately keeps readable (a path, a field name) can carry
// ESC, and an escape sequence printed to the operator's terminal can clear the
// screen, move the cursor back over rows already shown, or open an OSC-8 hyperlink.
// The resolvable sources of such a line are all disk-borne: the engine's
// per-destination Detail reads the live hooks.json (internal/integrations
// hookLiveCommandPosture), the composed consent diff carries the same sentences, the
// rollback and repair arms print the composed engine lines and notes, a failed step
// reports its own error, and the settings ledger's field-name error rows reach the
// same printer (agysettings refuses an undeclared field by naming it). This door
// therefore neutralises at PRINT time, once per sink, and every sink that prints a
// value read out of a user-editable document routes it through stripTerminalControl
// (directly, or through transcriptError / transcriptRow / stepFailureRow): the status
// rows, the diff rows under both gates, the "nothing to write / roll back / repair"
// rows, and the error rows. A future builder feeding one of those sinks is covered by
// construction instead of one fix per builder. What stays raw is fixed engine
// vocabulary (feature, verb, intent, preset and RollbackSkip words), %q-quoted CLI
// operands, and the engine's own deploy and settings paths, which it composes from the
// environment instead of reading out of a document.
//
// The rule is the TUI frame renderer's (internal/tui/render.go runeSurvivesStrip):
// tab survives because it is legitimate layout, Cc controls and Cf format
// characters (bidi overrides U+202A-U+202E / U+2066-U+2069, zero-width joiners and
// spaces, BOM) are dropped because a disk-borne right-to-left override or zero-width
// rune can reorder or spoof a rendered line without being a Cc control. Newlines are
// Cc and are dropped too, so a crafted value cannot inject extra transcript lines.
// A local copy rather than a shared export: the same predicate is unexported in
// internal/tui and internal/hybrid today, and a new shared package for these sinks
// is a refactor this unit does not own. Keep the expressions in step.
func stripTerminalControl(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return !runeSurvivesTranscript(r) }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if !runeSurvivesTranscript(r) {
			return -1
		}
		return r
	}, s)
}

// runeSurvivesTranscript is the one survival rule stripTerminalControl applies
// (the TUI's runeSurvivesStrip, restated here so the two sites read alike).
func runeSurvivesTranscript(r rune) bool {
	return r == '\t' || (!unicode.IsControl(r) && !unicode.Is(unicode.Cf, r))
}

// transcriptError renders an error for a transcript line under the same rule. Error
// text is not exempt from F-S9F-1: the settings ledger refuses an undeclared field by
// naming it (agysettings), and that name is read out of the user-editable
// settings.json, so an explicit-door apply on such a document would otherwise print
// the escape sequence the diff sinks now strip.
func transcriptError(err error) string {
	if err == nil {
		return ""
	}
	return stripTerminalControl(err.Error())
}

// transcriptRow renders one indented transcript row (a composed diff line, a rollback
// line, a repair note) under the same rule. The engine's own line is not exempt: a
// repair note can carry the settings ledger's sentence, which names a field out of the
// live settings.json, and the "nothing to write / roll back / repair" arms print those
// rows with no gate in front of them.
func transcriptRow(text string) string {
	return "  " + stripTerminalControl(text)
}

// stepFailureRow renders a failed step's transcript row. The feature word and the
// action are fixed engine vocabulary; the error is not, so it goes through
// transcriptError like every other dynamic value this door prints.
func stepFailureRow(feature integrations.Feature, action string, err error) string {
	return fmt.Sprintf("[agy-pro] %s %s: %s", feature, action, transcriptError(err))
}
