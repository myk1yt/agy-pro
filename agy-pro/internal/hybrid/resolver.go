package hybrid

import (
	"strings"

	"agypro/internal/providers"
)

// RegistryResolver returns the mesh-side model allow-list seam for
// wireadp.Config.ResolveModel. It implements spec §3.3's resolution order over
// the registry's ENABLED models:
//
//  1. exact slug match (the model id as agy sends it on the wire);
//  2. terminal-segment match: the segment after the last "/models/" when present
//     (the customModels scheme shapes, e.g. "gemini-api://host:port/models/<id>",
//     "evergreen:host:port/models/<id>", "blade:host:port/models/<id>"), else after
//     the last "/";
//  3. not found.
//
// The resolver reports found/not-found only. Housekeeping and the 404 are
// wireadp's layer, and the two housekeeping rules sit on OPPOSITE sides of this
// seam: the prompt-signature title-turn rule (isTitleTurn) runs BEFORE
// resolveModelName, so a title turn never reaches this resolver; the model-name
// family (IsHousekeepingName) runs AFTER, on a miss from this seam. Both are
// answered in wireadp and neither resolves to a target, so a (zero, false, nil)
// return is an allow-list miss, not an error. A nil registry resolves nothing.
func RegistryResolver(reg *providers.Registry) func(name string) (providers.Target, bool, error) {
	return func(name string) (providers.Target, bool, error) {
		target, ok := resolveSlug(reg, name)
		return target, ok, nil
	}
}

// resolveSlug applies the §3.3 order: exact slug, then terminal segment.
func resolveSlug(reg *providers.Registry, name string) (providers.Target, bool) {
	if reg == nil || name == "" {
		return providers.Target{}, false
	}
	if target, ok := findSlug(reg, name); ok {
		return target, true
	}
	if terminal := terminalSegment(name); terminal != name {
		if target, ok := findSlug(reg, terminal); ok {
			return target, true
		}
	}
	return providers.Target{}, false
}

// terminalSegment returns the segment after the last "/models/" when present,
// else after the last "/", else the name unchanged.
func terminalSegment(name string) string {
	if i := strings.LastIndex(name, "/models/"); i >= 0 {
		return name[i+len("/models/"):]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// findSlug returns the first enabled model whose id equals slug, in registry
// order. Enabled-only matches what the catalog listing exposes. The id is
// normalized identically to the catalog on BOTH sides — trim AND strip control
// characters (FIX-49 trim, FIX-64 control chars) — so the resolver matches the
// same key the catalog lists (a padded or control-char id would otherwise be
// listed but 404 on the wire).
func findSlug(reg *providers.Registry, slug string) (providers.Target, bool) {
	slug = stripControlChars(strings.TrimSpace(slug))
	if slug == "" {
		return providers.Target{}, false
	}
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if stripControlChars(strings.TrimSpace(m.ID)) == slug && m.IsEnabled() {
				return providers.Target{Provider: p, Model: m}, true
			}
		}
	}
	return providers.Target{}, false
}
