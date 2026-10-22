package settings

// Read-only marker shared by every settings surface (P1-4). One spelling
// ("ro") renders everywhere: the legacy panel prefixes it with a status dot
// ("● ro"), the `show` snapshot and the setup TUI frame it in brackets
// ("[ro]"). No surface invents its own wording.

// ReadOnlyMarker is the one shared read-only spelling.
const ReadOnlyMarker = "ro"

// ReadOnlyLegend is the one-line legend explaining the marker, printed
// verbatim by the surfaces that cannot reach the i18n bundle. The legacy
// interactive panel prints this const in its footer (settings/tui.go). The
// `show` snapshot does NOT print the const: it composes its own flag line,
// "Flags: [ro] = read-only (display only; pressing the row changes nothing)
// · [danger] = writes ask y/n first" (settings/run.go), which repeats this
// sentence as one clause of a two-flag legend. Keep the two in step: the
// marker stays one spelling, the legend sentence is shared text, but only the
// panel footer renders the const byte-for-byte. The setup TUI resolves the
// same sentence through its catalog (settings.legend.readonly) so the legend
// follows the shell language there.
//
// Catalog boundary (S7-C7 convention, the precedent for the rule): this
// user-visible string lives OUTSIDE the twelve i18n catalogs on purpose, as a
// package const. Revisit when the P5 unified settings surface exists.
const ReadOnlyLegend = "ro = read-only (display only; pressing the row changes nothing)"
