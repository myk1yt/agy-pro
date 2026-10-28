// ---------------------------------------------------------------------------
// P5-4 deferred item: no Route switch in the launcher is exhaustive
// ---------------------------------------------------------------------------
//
// Discharges the deferred item recorded in
// docs/261022_0002_session_agy-pro-session8/impl-p54-posture.md §3 (the
// "switch decision.Route (launcher.go:82)" and "switch choice (launcher.go:86)"
// rows) and §5 item 6 ("T9 must add a `default:` arm or an exhaustive-check
// test when it adds a Route"). Both switches are default-less on purpose: the
// passthrough and native values MUST fall through to the historical routing
// that follows them. The cost of that design is silence - a new Route value
// would land on the legacy path instead of failing - so this file pins the
// sets instead: every constant must be either armed or recorded with the reason
// it is allowed to fall through.
//
// Nothing here changes behavior; it only turns a silent new value (or a
// deleted arm) into a test failure that names the sites to update.
package launcher

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// routePin is this file's hand-maintained name table for the launcher.Route
// values, in decide.go declaration order (RoutePassthrough == 0, one iota step
// per entry). It is written out by hand on purpose: a table derived from the
// code under test could never notice an addition to that code.
var routePin = []struct {
	route Route
	name  string
}{
	{RoutePassthrough, "RoutePassthrough"},
	{RouteNative, "RouteNative"},
	{RouteBridge, "RouteBridge"},
	{RouteMenu, "RouteMenu"},
	{RouteFanout, "RouteFanout"},
	{RouteHybridP1, "RouteHybridP1"},
	{RouteHybridP2, "RouteHybridP2"},
}

// pinnedRoutes lists every pinned value in declaration order.
func pinnedRoutes() []Route {
	out := make([]Route, len(routePin))
	for i, p := range routePin {
		out[i] = p.route
	}
	return out
}

// routePinName renders r through the pin table. The returned name is a
// placeholder when the value is not pinned, so a caller can still print it.
func routePinName(r Route) (name string, pinned bool) {
	for _, p := range routePin {
		if p.route == r {
			return p.name, true
		}
	}
	return fmt.Sprintf("Route(%d)", int(r)), false
}

// routePinValue reverses routePinName: the value a declared constant names.
func routePinValue(name string) (Route, bool) {
	for _, p := range routePin {
		if p.name == name {
			return p.route, true
		}
	}
	return 0, false
}

// routeDisplay is the form every failure message uses: name plus value.
func routeDisplay(r Route) string {
	name, _ := routePinName(r)
	return fmt.Sprintf("%s(%d)", name, int(r))
}

// TestRouteConstantSetPinned refuses a launcher.Route block this file has not
// been told about. Adding a value to decide.go is the event P5-4 warned about,
// so it has to touch this pin in the same change.
func TestRouteConstantSetPinned(t *testing.T) {
	declared, err := declaredRouteConstants("decide.go")
	if err != nil {
		t.Fatalf("decide.go: %v (this pin reads the production source; a renamed file must update this test)", err)
	}

	var diffs []string
	if len(declared) != len(routePin) {
		diffs = append(diffs, fmt.Sprintf("decide.go declares %d launcher.Route constants; this pin knows %d",
			len(declared), len(routePin)))
	}
	for i := 0; i < len(declared) || i < len(routePin); i++ {
		switch {
		case i >= len(declared):
			diffs = append(diffs, fmt.Sprintf("  #%d: this pin expects %s = %d, decide.go declares nothing (the const block is shorter)",
				i, routePin[i].name, int(routePin[i].route)))
		case i >= len(routePin):
			diffs = append(diffs, fmt.Sprintf("  #%d: decide.go declares %s, this pin has no such entry", i, declared[i]))
		case declared[i] != routePin[i].name:
			diffs = append(diffs, fmt.Sprintf("  #%d: decide.go declares %s, this pin expects %s",
				i, declared[i], routePin[i].name))
		case routePin[i].route != Route(i):
			diffs = append(diffs, fmt.Sprintf("  #%d: this pin says %s = %d; the declaration order and the iota block disagree",
				i, routePin[i].name, int(routePin[i].route)))
		}
	}
	if len(diffs) == 0 {
		return
	}

	t.Fatalf("the launcher.Route constant set changed and this pin was not updated.\n%s\n\n"+
		"A new Route value does not fail loudly on its own: the switches over Route carry no `default:` arm "+
		"(P5-4 §3), so an unhandled value falls through to the legacy path and the invocation looks normal. "+
		"If this change is intentional, handle the value at every Route switch (%s) with a `case` arm or a "+
		"`default:` arm, then record it in routePin and routeSwitchSites() in route_pin_test.go.\n"+
		"(Discharges the P5-4 deferred item: docs/261022_0002_session_agy-pro-session8/impl-p54-posture.md "+
		"§3 + §5 item 6.)", strings.Join(diffs, "\n"), liveRouteSwitches())
}

// parsedSwitch is one switch statement read back out of a production file.
type parsedSwitch struct {
	tag        string   // rendered tag expression: "decision.Route", "choice", "r", ...
	pos        string   // "launcher.go:82"
	arms       []string // case arm identifiers, sorted
	hasDefault bool     // a `default:` clause exists (its List is empty)
}

// routeSwitchSite is one production switch over launcher.Route values, as this
// pin records it: every constant decide.go declares must appear in exactly one
// of the three buckets.
type routeSwitchSite struct {
	name string // used in every failure message
	file string // package source file holding the switch
	tag  string // the switch's tag expression, rendered
	// handled holds the constants with an explicit `case` arm.
	handled []Route
	// fallThrough holds reachable constants with NO arm, plus why the code
	// after the switch is the right behavior for them.
	fallThrough map[Route]string
	// unreachable holds constants the switch cannot receive, plus the
	// mechanism that makes that true.
	unreachable map[Route]string
	// reachable, when set, probes which values can actually arrive; the
	// unreachable claims and the handled arms are then checked against it
	// instead of being trusted as prose.
	reachable func() map[Route]bool
}

func routeSwitchSites() []routeSwitchSite {
	return []routeSwitchSite{
		{
			name:    "dispatch switch (launcher.go:82)",
			file:    "launcher.go",
			tag:     "decision.Route",
			handled: []Route{RouteMenu, RouteBridge, RouteHybridP1, RouteHybridP2},
			fallThrough: map[Route]string{
				RoutePassthrough: "historical routing by design: the code after the switch runs ShouldActivateBridge " +
					"(--bridge/-b/AGY_PRO_BRIDGE/AGY_BRIDGE, or a --model naming a registered model) to choose native vs bridge",
				RouteNative: "the code after the switch is the native path: no launcher, no gateway env " +
					"(forcedNativeID empty, ShouldActivateBridge false)",
			},
			unreachable: map[Route]string{
				RouteFanout: "decideRoute never returns it (decide.go:187-227); RouteFanout is menu-owned and only " +
					"ResolveMenuChoice produces it (decide.go:233-242)",
			},
		},
		{
			name:    "menu choice switch (launcher.go:86)",
			file:    "launcher.go",
			tag:     "choice",
			handled: []Route{RouteBridge, RouteFanout},
			fallThrough: map[Route]string{
				RouteNative: "the intended default: [1], an empty line and a menu timeout all mean native, and the " +
					"code after the switch is the native/passthrough path",
			},
			unreachable: map[Route]string{
				RoutePassthrough: "ResolveMenuChoice returns only native/bridge/fanout (decide.go:233-242)",
				RouteMenu:        "the menu returns a choice, never a request to show the menu again",
				RouteHybridP1:    "the hybrid front doors are Decide's verdict, not a menu choice (decide.go:198-207, 233-242)",
				RouteHybridP2:    "the hybrid front doors are Decide's verdict, not a menu choice (decide.go:198-207, 233-242)",
			},
			reachable: menuChoiceReachable,
		},
		{
			name:    "route renderer switch (decide.go:42)",
			file:    "decide.go",
			tag:     "r",
			handled: pinnedRoutes(),
		},
	}
}

// TestRouteSwitchArmsPinned compares each recorded switch against the production
// source and against the pinned constant set. It fails on a changed arm, on a
// constant with no bucket, and on a bucket a value appears in twice.
func TestRouteSwitchArmsPinned(t *testing.T) {
	for _, site := range routeSwitchSites() {
		t.Run(site.name, func(t *testing.T) {
			sw, err := findSwitch(site.file, site.tag)
			if err != nil {
				t.Fatalf("%s: %v\nThis pin reads the production source. If the switch moved or was renamed, "+
					"update routeSwitchSites() in route_pin_test.go. Live Route switches: %s.",
					site.name, err, liveRouteSwitches())
			}

			// 1. The explicit arms must be exactly the ones this pin records.
			wantArms := make([]string, 0, len(site.handled))
			for _, r := range site.handled {
				name, ok := routePinName(r)
				if !ok {
					t.Fatalf("%s: records %s in `handled`, but this pin has no entry for that value; add it to routePin",
						site.name, routeDisplay(r))
				}
				wantArms = append(wantArms, name)
			}
			sort.Strings(wantArms)
			if got := strings.Join(sw.arms, ","); got != strings.Join(wantArms, ",") {
				t.Fatalf("%s: `switch %s` at %s has case arms %v; this pin records %v.\n"+
					"If the arm change is intentional, update routeSwitchSites() in route_pin_test.go. "+
					"A Route value with neither a `case` arm nor a `default:` arm falls through silently, "+
					"which is the failure this pin exists to prevent (P5-4 §3).",
					site.name, site.tag, sw.pos, sw.arms, wantArms)
			}

			// 2. Every constant decide.go declares is accounted for exactly once.
			//    decide.go is the authority for which values exist, so this half
			//    bites even when routePin was forgotten.
			declared, err := declaredRouteConstants("decide.go")
			if err != nil {
				t.Fatalf("%s: decide.go: %v", site.name, err)
			}
			declaredSet := make(map[string]bool, len(declared))
			bucket := make(map[Route]string, len(declared))
			record := func(r Route, where, why string) {
				t.Helper()
				if _, pinned := routePinName(r); !pinned {
					t.Fatalf("%s: %s records %s, which is not a pinned launcher.Route value; "+
						"routePin and routeSwitchSites() disagree", site.name, where, routeDisplay(r))
				}
				if strings.TrimSpace(why) == "" && where != "handled" {
					t.Fatalf("%s: records %s in `%s` with no reason; an unexplained fall-through is exactly the "+
						"silence this pin removes", site.name, routeDisplay(r), where)
				}
				if prev, dup := bucket[r]; dup {
					t.Fatalf("%s: records %s twice (`%s` and `%s`); each value may be armed, fall through, "+
						"or be unreachable - never two of them", site.name, routeDisplay(r), prev, where)
				}
				bucket[r] = where
			}
			for _, r := range site.handled {
				record(r, "handled", "")
			}
			for r, why := range site.fallThrough {
				record(r, "fallThrough", why)
			}
			for r, why := range site.unreachable {
				record(r, "unreachable", why)
			}
			for _, name := range declared {
				declaredSet[name] = true
				r, pinned := routePinValue(name)
				if !pinned {
					t.Fatalf("%s: decide.go declares %s, which this pin has no entry for; add it to routePin and "+
						"place it in a bucket at this site", site.name, name)
				}
				if _, ok := bucket[r]; ok {
					continue
				}
				t.Fatalf("%s: %s is neither armed by `switch %s` at %s nor recorded as fall-through or "+
					"unreachable.\nThat is the new-Route-value case P5-4 §3 warns about: decide whether the switch "+
					"must handle it (`handled`), or the code after the switch already does (`fallThrough`, with the "+
					"reason), and update routeSwitchSites() in route_pin_test.go.",
					site.name, routeDisplay(r), site.tag, sw.pos)
			}
			for r, where := range bucket {
				name, _ := routePinName(r)
				if !declaredSet[name] {
					t.Fatalf("%s: records %s in `%s`, but decide.go no longer declares it; the pin is stale",
						site.name, routeDisplay(r), where)
				}
			}

			// 3. A `default:` arm retires the recorded fall-through.
			if sw.hasDefault && len(site.fallThrough) > 0 {
				t.Fatalf("%s: `switch %s` at %s now has a `default:` arm, so the fall-through this pin records "+
					"for %v no longer happens. That is the safer shape (P5-4 §3 asks for it): delete those "+
					"`fallThrough` entries from routeSwitchSites(), or narrow them to the values the default arm "+
					"still sends down the historical path.", site.name, site.tag, sw.pos, mapKeys(site.fallThrough))
			}

			// 4. Where a probe exists, the "unreachable" claims are checked
			//    against behavior instead of trusted as prose.
			if site.reachable == nil {
				return
			}
			seen := site.reachable()
			for r := range site.unreachable {
				if seen[r] {
					t.Fatalf("%s: this pin records %s as unreachable, but the probe produced it; the recorded "+
						"reason is wrong and the switch may need an arm for it", site.name, routeDisplay(r))
				}
			}
			for _, r := range site.handled {
				if !seen[r] {
					t.Fatalf("%s: `switch %s` at %s arms %s, but the probe never produced it; either the arm is "+
						"dead or the probe corpus in menuChoiceReachable() is stale",
						site.name, site.tag, sw.pos, routeDisplay(r))
				}
			}
			for r := range seen {
				if where, ok := bucket[r]; !ok || where == "unreachable" {
					t.Fatalf("%s: the probe produced %s, which this pin does not accept here (bucket %q)",
						site.name, routeDisplay(r), where)
				}
			}
		})
	}
}

// menuChoiceReachable probes ResolveMenuChoice for the values the menu can hand
// the `switch choice` arm: every answer the banner offers, the empty answers
// (Enter and timeout), and the near misses a stray keypress or a pasted word
// produces.
func menuChoiceReachable() map[Route]bool {
	corpus := []string{
		"", " ", "\t", "\x1b", "1", " 1 ", "2", " 2 ", "3", "0", "4", "9", "10",
		"n", "b", "s", "native", "bridge", "swarm", "menu", "gateway", "p1", "p2",
		"q", "quit", "x", "help", "--help",
	}
	seen := make(map[Route]bool, len(routePin))
	for _, in := range corpus {
		seen[ResolveMenuChoice(in)] = true
	}
	return seen
}

// parsedSwitchSites returns every tag-bearing switch statement in path.
func parseSwitchSites(path string) ([]parsedSwitch, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var out []parsedSwitch
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		p := fset.Position(sw.Pos())
		ps := parsedSwitch{tag: renderTagExpr(sw.Tag), pos: fmt.Sprintf("%s:%d", p.Filename, p.Line)}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			if len(cc.List) == 0 {
				ps.hasDefault = true
				continue
			}
			for _, e := range cc.List {
				if name := caseArmName(e); name != "" {
					ps.arms = append(ps.arms, name)
				}
			}
		}
		sort.Strings(ps.arms)
		out = append(out, ps)
		return true
	})
	return out, nil
}

// findSwitch returns the single switch in path whose tag renders to tag.
func findSwitch(path, tag string) (parsedSwitch, error) {
	sites, err := parseSwitchSites(path)
	if err != nil {
		return parsedSwitch{}, err
	}
	var hits []parsedSwitch
	for _, s := range sites {
		if s.tag == tag {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return parsedSwitch{}, fmt.Errorf("no `switch %s` in %s (%d switches found)", tag, path, len(sites))
	default:
		pos := make([]string, 0, len(hits))
		for _, h := range hits {
			pos = append(pos, h.pos)
		}
		return parsedSwitch{}, fmt.Errorf("%s holds %d `switch %s` statements (%s); this pin expects exactly one",
			path, len(hits), tag, strings.Join(pos, ", "))
	}
}

// declaredRouteConstants lists the launcher.Route constant names decide.go
// declares, in declaration order.
//
// The go/ast walk collects a const block from the first spec that names the
// type `Route` to the end of that block, which is how an iota block declares its
// whole set (only the first spec in decide.go spells the type out). A later
// block that re-declares Route constants is collected the same way.
func declaredRouteConstants(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var names []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		inRouteBlock := false
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "Route" {
				inRouteBlock = true
			}
			if inRouteBlock {
				for _, n := range vs.Names {
					names = append(names, n.Name)
				}
			}
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no `Route`-typed const block found")
	}
	return names, nil
}

// renderTagExpr renders a switch tag the way routeSwitchSites() spells it.
func renderTagExpr(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if inner := renderTagExpr(x.X); inner != "" {
			return inner + "." + x.Sel.Name
		}
	case *ast.ParenExpr:
		return renderTagExpr(x.X)
	}
	return ""
}

// caseArmName renders one case expression as the constant's own name, so an
// in-package `RouteBridge` and an out-of-package `launcher.RouteBridge` compare
// equal.
func caseArmName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.ParenExpr:
		return caseArmName(x.X)
	}
	return ""
}

// liveRouteSwitches renders every switch this pin tracks as "file:line
// (`switch tag`)", read live from the source, so a failure message points at
// the current position even when lines moved above the switch.
func liveRouteSwitches() string {
	var parts []string
	for _, site := range routeSwitchSites() {
		sites, err := parseSwitchSites(site.file)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%s (unparsed: %v)", site.file, err))
			continue
		}
		for _, s := range sites {
			if s.tag == site.tag {
				parts = append(parts, fmt.Sprintf("%s (`switch %s`)", s.pos, s.tag))
			}
		}
	}
	if len(parts) == 0 {
		return "none found"
	}
	return strings.Join(parts, ", ")
}

// mapKeys lists a route->reason map's values in a stable order.
func mapKeys(m map[Route]string) []string {
	out := make([]string, 0, len(m))
	for r := range m {
		out = append(out, routeDisplay(r))
	}
	sort.Strings(out)
	return out
}
