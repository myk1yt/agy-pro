// Package inventory is the single source of truth for the combined model
// view: the native `agy models` catalog plus every enabled model in the
// user's providers registry (contract §2.5). Consumers: `agy pro models
// --all`, the launcher model sub-menu, and the swarm builder.
package inventory

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"agypro/internal/providers"
)

// Entry is one row of the combined inventory (contract §2.5).
type Entry struct {
	Number       int    // 1-based, stable = render order
	Kind         string // "native" | "provider"
	Group        string // "native" or provider name ("google","openrouter","bigmodel",...) from the USER'S registry
	ModelID      string // bare id
	DisplayName  string // native: from `agy models` col 2; provider: providers.DisplayName
	Capabilities string // provider: Model.CapabilitySummary(); native: "" (unknown - do not fabricate)
	Spec         string // canonical seat token: "native:<id>" or "<provider>/<id>"
	KeyMissing   bool   // provider entry whose APIKeyEnv name is set but resolves empty
	// Family is the family the fanout engine will use for this entry: the
	// explicit registry family CASE-PRESERVED (C2: display sinks mask
	// through the uppercase-requiring decorated class, so the folded
	// FamilyName() would ride a lowercase-shaped secret raw), else
	// FamilyName() (the id-prefix inference) for provider rows,
	// InferFamily(id) for native rows. Rotation consumers re-fold with
	// entryFamily (strings.ToLower), so the case never reaches fanout's
	// family matching. Carrying it here is what keeps preset family
	// rotation aligned with fanout's own rotation - the id-prefix inference
	// used before A7 ignored a registry-declared family and could
	// double-seat one declared family in a "family-rotated" preset.
	Family string
	// Excluded is the unified pool-exclusion verdict for this row, computed
	// ONCE at Load so no consumer re-derives it (P4-2 unification of the two
	// disagreeing predicates: providers.IsK3Family saw an explicit family,
	// the retired inventory.IsK3Excluded saw only an id token, so a benign id
	// declared `family: k3` was excluded in fanout auto-seating and allowed
	// in swarm preset expansion).
	//
	// The one rule (user decision D2 = b): auto-seating and failover SKIP an
	// excluded row; an explicit pick (a numbered seat or a spec) is HONORED
	// WITH A WARNING; nothing is ever rejected. The k3 constant is folded in
	// as the built-in default entry of the user-owned list
	// (defaults.excludeModels, D1 = a) - there is no separate stricter path.
	//
	// JSON name is lowercase `excluded` for the `agy pro models --all --json`
	// dump (pro_models.go marshals []Entry).
	Excluded bool `json:"excluded"`
}

// NativeModel is one row of the real `agy models` catalog.
type NativeModel struct{ ID, DisplayName string }

// Inventory is the combined catalog snapshot (contract §2.5).
type Inventory struct {
	Entries     []Entry
	NativeErr   error // non-nil -> native group rendered as unavailable (honest degrade)
	GeneratedAt time.Time
}

// Group is one display group: native first, then providers in registry order.
type Group struct {
	Name    string
	Entries []Entry
}

// Load builds the combined inventory. reg may be nil (native-only view).
// nf may be nil (registry-only view). A native fetch failure is recorded in
// Inventory.NativeErr and never fails the load: the external rows stay
// available and the native group degrades honestly (contract §2.5).
func Load(ctx context.Context, reg *providers.Registry, nf NativeFetcher) (*Inventory, error) {
	inv := &Inventory{GeneratedAt: time.Now()}

	// Exclusion policy: one verdict per row, from the ONE authority (D1 = a:
	// `defaults.excludeModels` in providers.json, with the k3 rule folded in
	// as the built-in default entry). A nil reg (native-only view) still
	// evaluates the policy with an EMPTY defaults block rather than skipping
	// the flag: the built-in default entry then applies identically on both
	// views, so a native row that matches the built-in rule is marked the
	// same way a provider row is, and Entry.Excluded is never a
	// provider-only-meaningful field.
	policy := providers.ExclusionPolicyFromDefaults(providers.Defaults{})
	if reg != nil {
		policy = providers.ExclusionPolicyFromDefaults(reg.Defaults)
	}

	var native []NativeModel
	if nf != nil {
		models, err := nf.Fetch(ctx)
		if err != nil {
			inv.NativeErr = err
		} else {
			native = models
		}
	}

	// Native group: `agy models` output order; exact-id duplicates collapse
	// (identity = (Kind, Group, lower(ModelID)), contract §2.5 dedupe).
	seenNative := make(map[string]bool, len(native))
	for _, m := range native {
		key := strings.ToLower(strings.TrimSpace(m.ID))
		if key == "" || seenNative[key] {
			continue
		}
		seenNative[key] = true
		// A7: the same inference the native seat target uses
		// (fanout nativeTarget -> providers.InferFamily).
		fam := providers.InferFamily(m.ID)
		inv.Entries = append(inv.Entries, Entry{
			Kind:        "native",
			Group:       "native",
			ModelID:     m.ID,
			DisplayName: m.DisplayName,
			Spec:        "native:" + m.ID,
			Family:      fam,
			// Native rows evaluate the policy as ("native", id, family): the
			// same three operands a provider row passes, so a user list
			// naming "native" excludes native rows and a family entry hits
			// both views identically.
			Excluded: policy.Excludes("native", m.ID, fam),
		})
	}

	// Provider groups: providers.json order, models in registry order.
	// Only enabled models participate (B1-1 "all enabled registered externals").
	if reg != nil {
		for _, p := range reg.Providers {
			seenProv := make(map[string]bool, len(p.Models))
			for _, m := range p.Models {
				if !m.IsEnabled() {
					continue
				}
				key := strings.ToLower(strings.TrimSpace(m.ID))
				if key == "" || seenProv[key] {
					continue
				}
				seenProv[key] = true
				// KeyMissing: the provider declares a key env name but the
				// value resolves empty. A provider with no APIKeyEnv needs no
				// key and is never flagged.
				keyMissing := p.APIKeyEnv != "" && p.KeyValue() == ""
				// A7: familyForEntry honors an explicit registry family
				// before falling back to the id prefix, exactly like the
				// provider target the fanout engine builds for this row.
				// C2: the explicit family is carried CASE-PRESERVED so
				// display sinks (models --json) can mask a key-shaped
				// family through the uppercase-requiring decorated
				// class; entryFamily re-folds for rotation.
				fam := familyForEntry(m)
				inv.Entries = append(inv.Entries, Entry{
					Kind:         "provider",
					Group:        p.Name,
					ModelID:      m.ID,
					DisplayName:  providers.DisplayName(m.ID, p.Name),
					Capabilities: m.CapabilitySummary(),
					Spec:         p.Name + "/" + m.ID,
					KeyMissing:   keyMissing,
					Family:       fam,
					// Provider rows pass the provider NAME as the first
					// operand (Entry.Group) so a provider-level list entry
					// matches, and the carried Family so a `family: k3`
					// declaration on a benign id is excluded exactly like
					// the fanout side already excluded it.
					Excluded: policy.Excludes(p.Name, m.ID, fam),
				})
			}
		}
	}

	for i := range inv.Entries {
		inv.Entries[i].Number = i + 1
	}
	return inv, nil
}

// familyForEntry is the family an inventory entry carries: the explicit
// registry family, case-preserved (C2), else the folded FamilyName()
// (explicit or id-inferred). Rotation consumers re-fold through entryFamily,
// so the case change never reaches fanout's family matching.
func familyForEntry(m providers.Model) string {
	if f := strings.TrimSpace(m.Family); f != "" {
		return f
	}
	return m.FamilyName()
}

// Groups returns entries grouped for display in fixed order: [native] first,
// then providers in registry order. The native group is always present (with
// zero entries on degrade) so consumers can render the honest-unavailable note.
func (inv *Inventory) Groups() []Group {
	if inv == nil {
		return nil
	}
	var out []Group
	cur := -1
	for _, e := range inv.Entries {
		if cur < 0 || out[cur].Name != e.Group {
			out = append(out, Group{Name: e.Group})
			cur = len(out) - 1
		}
		out[cur].Entries = append(out[cur].Entries, e)
	}
	if len(out) == 0 {
		out = append(out, Group{Name: "native"})
	}
	return out
}

// RenderNumbered writes the numbered inventory rows (contract §2.7 format):
//
//	[ 1] gemini-3.8-flash-high        Gemini 3.8 Flash (High)          [native]
//	[19] gemini-2.5-flash             tools, vision                    [google]
//	[23] glm-4.6                      tools, thinking(default)         [bigmodel] (key missing)      excluded (exclusion list)
//
// Rows are numbered globally in render order. The middle column shows the
// capability summary for provider entries and the display name for native
// entries (native capabilities are unknown and never fabricated).
//
// D7: an excluded row carries providers.ExcludedMarker() as a trailing suffix,
// in the same order and with the same separator as the swarm seat list
// (internal/swarm/builder.go RenderSeatList): "(key missing)" first, then six
// spaces, then the marker, so a row can carry both and the two surfaces read
// identically. The standing rule tells an agent to pick seats from this list,
// so this list must show which rows the exclusion list covers (auto-seating and
// failover skip them; an explicit pick is honored with a warning).
//
// The suffix rides AFTER the group tag, outside both padded columns, so the
// column widths (computed from the masked id / mid cells only) and the frame
// are unchanged by the marker. No truncation logic exists on this path: the
// columns are `%-*s` right-padding only, so a longer label never needs one.
func (inv *Inventory) RenderNumbered(out io.Writer) {
	if inv == nil {
		return
	}
	idW, midW := 0, 0
	for _, e := range inv.Entries {
		// K5: widths are computed from the MASKED strings, the same text the
		// cells render, so a row whose raw id/capabilities are long but mask
		// short does not pad every other row past its rendered text.
		if n := len(providers.MaskSlot(e.ModelID)); n > idW {
			idW = n
		}
		if n := len(maskMidColumn(e)); n > midW {
			midW = n
		}
	}
	for _, e := range inv.Entries {
		num := fmt.Sprintf("[%d]", e.Number)
		if e.Number < 10 {
			num = fmt.Sprintf("[ %d]", e.Number)
		}
		suffix := ""
		if e.KeyMissing {
			suffix = "      (key missing)"
		}
		if e.Excluded {
			// D7 + sibling parity: the marker is the shared constant, appended
			// after "(key missing)" with the same six-space separator the swarm
			// seat list uses, so both listings of the same entry end identically.
			suffix += "      " + providers.ExcludedMarker()
		}
		// G6/S5/K1: the model ID, the mid column, and the group tag are
		// registry-derived display slots, so all three route through
		// providers.MaskSlot, the one registry-slot display authority. This
		// listing used to ride MaskOperand: its location exemption printed a
		// "./<key>"-shaped id raw and its classifiers predate the shared
		// policy's look-alike class, so the human listing and its models --json
		// twin disagreed about the same record. The slot masker keeps benign
		// rows byte-identical and adds the policy fallback (a look-alike
		// separator, an inner plant behind an ASCII marker) plus the
		// composed-label arm. The capability text of a provider row embeds the
		// registry-persisted reasoningEffort (Capabilities.Summary composes
		// thinking(<effort>)), so the mid column masks it as well.
		fmt.Fprintf(out, "%s %-*s  %-*s  [%s]%s\n", num, idW, providers.MaskSlot(e.ModelID), midW, maskMidColumn(e), providers.MaskSlot(e.Group), suffix)
	}
}

// maskMidColumn renders the middle column with its display text masked (S5):
// the native display name comes from the `agy models` catalog and can carry
// a key-shaped value, so it routes through the registry-slot masker; the
// provider capability text embeds the registry-controlled reasoningEffort
// (nothing on the add-json / hand-edit write path validates that field), so it
// routes through the same masker, which starts with the operand rules
// Capabilities.Summary applies at the composition (defense in depth).
func maskMidColumn(e Entry) string {
	if e.Kind == "native" {
		return providers.MaskSlot(e.DisplayName)
	}
	return providers.MaskSlot(e.Capabilities)
}
