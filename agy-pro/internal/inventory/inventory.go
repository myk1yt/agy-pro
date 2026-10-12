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
	Group        string // "native" or provider name ("google","nw","bigmodel",...) from the USER'S registry
	ModelID      string // bare id
	DisplayName  string // native: from `agy models` col 2; provider: providers.DisplayName
	Capabilities string // provider: Model.CapabilitySummary(); native: "" (unknown - do not fabricate)
	Spec         string // canonical seat token: "native:<id>" or "<provider>/<id>"
	KeyMissing   bool   // provider entry whose APIKeyEnv name is set but resolves empty
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
		inv.Entries = append(inv.Entries, Entry{
			Kind:        "native",
			Group:       "native",
			ModelID:     m.ID,
			DisplayName: m.DisplayName,
			Spec:        "native:" + m.ID,
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
				inv.Entries = append(inv.Entries, Entry{
					Kind:         "provider",
					Group:        p.Name,
					ModelID:      m.ID,
					DisplayName:  providers.DisplayName(m.ID, p.Name),
					Capabilities: m.CapabilitySummary(),
					Spec:         p.Name + "/" + m.ID,
					KeyMissing:   keyMissing,
				})
			}
		}
	}

	for i := range inv.Entries {
		inv.Entries[i].Number = i + 1
	}
	return inv, nil
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
//	[23] glm-5.3-flash-flex           tools, thinking(default)         [nw]      (key missing)
//
// Rows are numbered globally in render order. The middle column shows the
// capability summary for provider entries and the display name for native
// entries (native capabilities are unknown and never fabricated).
func (inv *Inventory) RenderNumbered(out io.Writer) {
	if inv == nil {
		return
	}
	idW, midW := 0, 0
	for _, e := range inv.Entries {
		if n := len(e.ModelID); n > idW {
			idW = n
		}
		if n := len(midColumn(e)); n > midW {
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
		fmt.Fprintf(out, "%s %-*s  %-*s  [%s]%s\n", num, idW, e.ModelID, midW, midColumn(e), e.Group, suffix)
	}
}

// midColumn picks the rendered middle column for one entry: the capability
// summary for provider rows, the display name for native rows (contract §2.7
// sample lines).
func midColumn(e Entry) string {
	if e.Kind == "native" {
		return e.DisplayName
	}
	return e.Capabilities
}

// IsK3Excluded reports whether a bare model id belongs to the k3 family
// excluded from subagent/swarm AUTO-seating (CEO kickoff; plan §4.6).
// Id-only contexts delegate to the single source of truth
// providers.HasK3Token (contract §2.5b, F-C).
func IsK3Excluded(modelID string) bool {
	return providers.HasK3Token(modelID)
}
