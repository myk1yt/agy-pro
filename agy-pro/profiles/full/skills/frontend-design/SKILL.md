---
name: frontend-design
description: >
  Unified frontend design framework (Taste + Impeccable + Emil Kowalski): design dials, anti-slop rules, typography, color, spatial, motion, copy, performance, accessibility, and review. Use when building, reviewing, or redesigning frontend interfaces, components, or pages.
---

# Frontend Design (Unified Framework)

A single, opinionated design framework that prevents "AI slop", the
generic, centered-card, cyan-on-dark, Inter-on-gray output that users
feel but cannot name. Combines Taste Skill (direction), Impeccable
(craft), and Emil Kowalski (motion) into one decision flow.

**Flow:** Set dials → Apply anti-slop rules → Build design system map →
Execute typography/color/spatial/motion/copy → Run review checklist.

---

## 1. Core Philosophy

Three beliefs that shape every decision in this skill:

- **"AI slop" is the failure mode.** Not a bug. The visible result of
  generating UI without an opinion. Users cannot name it, but they
  stop trusting it.
- **Taste is trained, not innate.** Comes from deliberate exposure to
  well-made interfaces and the discipline to reject the easy default.
  Tools do not give you taste. Constraint does.
- **Beauty is leverage.** A polished product earns patience. Users
  tolerate friction in software that respects them. They do not in
  software that looks like a template.

> If a design could be mistaken for any other AI's output, redesign it.

---

## 2. The Three Dials (Direction)

Before designing a project, screen, or component, set three dials.
These values are the project's *design signature*, write them down,
refer to them on every decision.

### 2.1 The Three Dials (unified)

| Range  | VARIANCE (layout)               | MOTION (movement)                      | DENSITY (info per screen)            |
|--------|---------------------------------|----------------------------------------|--------------------------------------|
| 1, 3    | Centered, symmetric, clean      | Hover states, subtle fades             | Spacious, one idea per screen        |
| 4, 6    | Off-grid, varied card sizes     | Page transitions, micro-interactions   | Standard content/whitespace ratio    |
| 7, 10   | Asymmetric, layered, broken grid| Magnetic cursors, scroll sequences     | Dense dashboards, multi-pane workspaces|

**Defaults:** product UI 4, 6 / 2, 4 / 4, 6 · marketing 6, 8 / 5, 7 / 2, 4 ·
analytics 4, 6 / 2, 4 / 6, 8 · content site 4, 6 / 5, 7 / 4, 6.

### 2.2 Dial rules

1. **Set once per project** (or per major screen group).
2. **Never set all three to 10.** Pick *one* dial to push to the extreme.
3. **Reference in code review.** "This hero pushes VARIANCE to 8 but
   DENSITY to 2, verify it still reads as a hero."
4. **Adjust, do not abandon.** Move the dial ±2 before redesigning.

---

## 3. Anti-Slop Rules (Decision Form)

Use as a final checklist before merging a UI change. Reject work that
hits two or more tells. For the *why*, see Section 4, 7.

### 3.1 Layout
- ❌ Identical card grids everywhere
- ❌ Centering everything, including body copy
- ❌ Wrapping every group of content in a card (cards-nested-in-cards)
- ✅ Mix left- and center-aligned *strategically*
- ✅ Use asymmetric layouts when VARIANCE ≥ 6
- ✅ Vary card sizes: one wide, two narrow, one full-bleed
- ✅ Break the grid intentionally (full-bleed hero, crossing sidebar)

### 3.2 Typography
- ❌ Inter / Roboto / Arial / system defaults for product UI
- ❌ One font size for everything except headings
- ❌ Every heading the same weight
- ❌ Monospace fonts as "developer vibes" (reserve for code only)
- ✅ At least **three** distinct type sizes per screen
- ✅ Mix weights for hierarchy (light, regular, semibold)
- ✅ Letter-spacing on uppercase labels, not on body copy

### 3.3 Color
- ❌ Only 2, 3 colors total; "AI palette" (cyan-on-dark, purple-to-blue)
- ❌ Pure `#000` / `#fff` surfaces; pure-gray neutrals
- ❌ Saturated brand color on every interactive element
- ❌ Gray text on colored backgrounds
- ✅ Tint your neutrals with the brand hue (1, 3% chroma)
- ✅ Use color for emphasis, not decoration (accent in 1, 2 states)
- ✅ Dark mode = same hues, desaturated 30, 50%, not inverted brightness

### 3.4 Components
- ❌ All buttons look identical
- ❌ Icon-plus-text pattern repeated on every row
- ❌ Large rounded icons above every heading (template smell)
- ✅ Vary button styles by importance (primary / secondary / ghost)
- ✅ Use whitespace as a design element, not a gap-filler
- ✅ Mix interactive and static elements in the same view

### 3.5 Motion
- ❌ Animating every state change
- ❌ Bounce / elastic easing
- ❌ Animating layout properties (`width`, `height`, `top`, `padding`)
- ❌ `transition: all`
- ✅ Animate `transform` and `opacity` only
- ✅ Animate the *one* transition that matters per screen
- ✅ Exits 20, 30% faster than entries

---

## 4. Typography Guide

Type is the loudest signal of taste. Most "AI slop" can be fixed by type alone.

### 4.1 Build a 3-Level Hierarchy (Minimum)

| Level   | Role                       | Cue                              |
|---------|----------------------------|----------------------------------|
| Display | Page hero, section opener  | Large, distinct weight           |
| Heading | Section / card title       | Medium, tighter                  |
| Body    | Paragraphs, descriptions   | Regular, generous `line-height`  |

Two levels is a wireframe. Five is noise. Three is design.

### 4.2 Font Pairing
- **One typeface, multiple weights** is safer than two.
- If pairing, pair a **serif with a sans** (different categories).
- **Contrast in x-height**, not just in style.
- Test at 14px body before committing.

### 4.3 Modular Scale

Pick a ratio (1.125, 1.2, 1.25, 1.333) and multiply. Do not eyeball sizes.

```css
:root {
  --step--1: 0.875rem;   /* caption */
  --step-0:  1rem;       /* body */
  --step-1:  1.25rem;    /* h3 */
  --step-2:  1.563rem;   /* h2 */
  --step-3:  1.953rem;   /* h1 */
  --step-4:  2.441rem;   /* display */
}
```

### 4.4 Vertical Rhythm

`line-height` should be a multiple of the base spacing unit (e.g.
4px grid + `line-height: 1.5`). Floating margin values (13px) break
the grid invisibly.

### 4.5 Fluid Type with `clamp()`

```css
h1 { font-size: clamp(1.75rem, 4vw, 3rem); }
```

Use `clamp()` for headings only. Body type needs consistent measure
for reading speed (`45, 75ch`).

---

## 5. Color & Contrast Guide

### 5.1 Use OKLCH

`oklch(lightness chroma hue)` gives perceptually uniform colors. Two
values with the same lightness look the same lightness, unlike HSL.
This makes tints and shades a 30-second task.

```css
:root {
  --bg:        oklch(98% 0.005 250);
  --bg-elev:   oklch(100% 0 0);
  --fg:        oklch(20% 0.01 250);
  --fg-muted:  oklch(45% 0.01 250);
  --accent:    oklch(60% 0.18 250);
}
```

### 5.2 Tinted Neutrals

Pure `#000` / `#fff` look like a wireframe. Tint every neutral with
1, 3% chroma of your brand hue. The result is "warm" or "cool"
surfaces that feel intentional.

### 5.3 Dark Mode

- Not an invert. A **separate palette**.
- Drop chroma in dark mode by 30, 50% (saturated colors vibrate on dark).
- Body text on dark should be ~`oklch(92% ...)` not pure white (shimmer
  effect at small sizes).

### 5.4 Contrast

- **WCAG AA** is the floor; **AAA** for body text (7:1 normal, 4.5:1 large).
- Test accent colors against background *and* muted-text background.
- Never rely on color alone to convey state, pair with icon, weight,
  or underline.

---

## 6. Spatial Design Guide

### 6.1 Spacing System

Pick **4px or 8px** as the base unit. Never use a raw pixel value for
margin, padding, or gap.

```css
:root {
  --space-1:  0.25rem;   /* 4  */
  --space-2:  0.5rem;    /* 8  */
  --space-3:  0.75rem;   /* 12 */
  --space-4:  1rem;      /* 16 */
  --space-6:  1.5rem;    /* 24 */
  --space-8:  2rem;      /* 32 */
  --space-12: 3rem;      /* 48 */
}
```

### 6.2 Hierarchy Through Spacing

The biggest lever is **the gap between a heading and its body**.
- Tight (`--space-1` to `--space-2`): they read as one unit.
- Loose (`--space-4` to `--space-6`): they read as two units.

Tight within a card; loose between sections. Same gap everywhere
reads as "one class for everything".

### 6.3 Grid Basics
- **12-column** for marketing; **16-column** for dashboards.
- Gutters `var(--space-4)` min, `var(--space-6)` preferred.
- **Max content width** of `~70ch` for body text.

---

## 7. Animation Decision Framework

Run every animation through these four questions, in order. Stop at
the first "no".

### Q1. Should this animate at all?

| Frequency of action       | Animation? | Why                                  |
|---------------------------|------------|--------------------------------------|
| Hundreds of times a day   | **No**     | Becomes noise. Speed wins.           |
| Many times a session      | **Yes**    | Small polish reads as quality.       |
| Once per session          | **Yes**    | Memorable moments deserve motion.    |
| First-time only           | **Yes**    | Onboarding sets the tone.            |

### Q2. What is the purpose?

State the job-to-be-done in one phrase. If you can't, the animation
is decoration.
- **Spatial consistency**, comes from where it was triggered
- **State indication**, open/closed, on/off
- **Feedback**, confirms action was received
- **Orientation**, helps user understand new layout
- **Delight**, only after the four above are satisfied

### Q3. What easing?

Never use the defaults. The default `ease` curve is the visual
signature of "this was added later".

```css
:root {
  --ease-out:       cubic-bezier(0.23, 1, 0.32, 1);   /* enter, exit, popover */
  --ease-in-out:    cubic-bezier(0.77, 0, 0.175, 1);   /* movement, reposition */
  --ease-drawer:    cubic-bezier(0.32, 0.72, 0, 1);    /* sheets, drawers */
  --ease-emphasized: cubic-bezier(0.2, 0, 0, 1);       /* material 3 style */
}
```

- **`ease-out`** for everything entering or exiting (user is waiting).
- **`ease-in-out`** for movement *within* the screen.
- **`ease-in`** almost never on UI, feels like fighting the user.

### Q4. How fast?

| Element            | Duration       | Notes                              |
|--------------------|----------------|------------------------------------|
| Button press       | 100, 160 ms     | Snappy. `transform: scale(0.97)`.  |
| Tooltip            | 125, 200 ms     | Slightly slower, has delay.        |
| Dropdown / popover | 150, 250 ms     | Slower than button.                |
| Modal / dialog     | 200, 500 ms     | Container fade + content ease.     |
| Page transition    | 200, 400 ms     | Never exceed 500 ms.               |

**Exits are 20, 30% faster than entries.** A slow exit feels like the
UI is clinging to the user.

---

## 8. Component Building Principles

### Buttons

```css
.button:active {
  transform: scale(0.97);
  transition: transform 80ms var(--ease-out);
}
```

- `scale(0.97)` is the sweet spot, visible feedback without theatrics.
- Never animate `background-color` on `:active`; combine with transform.

### Enter animations

**Never animate from `scale(0)`.** It feels broken and creates layout
flashes. Start from `scale(0.95)` + `opacity: 0`.

```css
.entry {
  transform: scale(0.95);
  opacity: 0;
  transition: transform 200ms var(--ease-out), opacity 200ms var(--ease-out);
}
.entry[data-state="open"] { transform: scale(1); opacity: 1; }
```

### Popovers and tooltips

**Origin-aware** positioning. The popover must grow *from* the trigger.

```css
.popover { transform-origin: var(--origin-x) var(--origin-y); }
```

Set the CSS variables in JS via `getBoundingClientRect()` on the
trigger. Never default to `transform-origin: center` for popovers.
For tooltips, **skip the delay on subsequent hovers** within a session.

### Blur to mask imperfect transitions

```css
.sheet-backdrop { backdrop-filter: blur(8px); background: rgba(0,0,0,0.4); }
```

Hides misalignment and is itself a quality signal.

### CSS transitions beat keyframes for UI

Keyframe animations cannot be interrupted. CSS transitions reverse
cleanly mid-flight when the user changes their mind.

| Use case                | Mechanism    |
|-------------------------|--------------|
| Hover, press, toggle    | `transition` |
| Loading, looping        | `@keyframes` |
| User-initiated state    | `transition` |
| System-driven motion    | `@keyframes` |

---

## 9. Performance Rules

1. **Only animate `transform` and `opacity`.** GPU-composited, never
   trigger layout. Animating `width`, `top`, `box-shadow` causes jank.
2. **CSS variables have inheritance cost.** Nested `--var` lookups
   multiply per frame. Keep custom property depth to 2, 3 levels.
3. **Framer Motion caveat.** Default `motion.div` may not promote to
   GPU layer. Set `willChange: "transform"` only during active animation.
4. **CSS beats JS under load.** `transition: transform 200ms` still
   hits its frame budget; `requestAnimationFrame` loops stutter.
5. **Avoid animating `box-shadow` and `filter`** on large surfaces.
   Animate the *parent's* opacity instead.

---

## 10. Accessibility (Motion)

### `prefers-reduced-motion`

```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
    scroll-behavior: auto !important;
  }
}
```

Replace the values, don't just disable. A 0ms transition still feels
intentional; a removed transition feels broken.

### Touch vs hover

Touch devices fire `:hover` on tap and *never* release it.

```css
@media (hover: hover) and (pointer: fine) {
  .card:hover { background: var(--bg-hover); }
}
```

### Animation on keyboard actions

Keyboard activation (Enter, Space) should not trigger celebratory
animations. The user is moving fast; get out of the way.

---

## 11. UX Writing Guide

Copy is design. Generic copy is a design anti-pattern.

- **Buttons**, Verb first ("Save changes"), specific over generic, no
  "Click here" / "Learn more" as the only label. Sentence case; no
  trailing periods.
- **Errors**, Format: *what happened* + *what to do*.
  ❌ "An error occurred." ✅ "Couldn't save your changes. Check your
  connection and try again." No apologetic filler ("Oops!", "Sorry!").
- **Empty states**, State the situation + show the action. Skip the
  illustration unless it adds meaning.
- **Microcopy**, Numbers in copy use `~30ms` not `~30 milliseconds`.
  Speed reads as confidence.

---

## 12. Design System Map (Project Commitment)

Produce this map **before** writing components. 15 minutes now prevents
weeks of inconsistent UI.

```yaml
# Design System Map (project-name)
colors:    { primary, secondary, accent, neutral-tinted, surface, background }
typography:{ display, heading, body, caption, font-pair: [heading, body] }
spacing:   { base: 4px, scale: [1, 2, 3, 4, 6, 8, 12, 16] }
components:
  button:     [primary, secondary, ghost]   # exactly 3 variants
  card:       [feature, quote, stat]        # vary by purpose
  input:      [default, error, disabled]
  navigation: [primary, utility]
```

**Map rules:**
- **Buttons: max 3 variants.** More than three and hierarchy collapses.
- **Cards: at least 2 distinct shapes.** A 3-up grid of identical cards
  is the single most common AI-slop tell.
- **Spacing: a scale, not arbitrary numbers.** No `13px` margins.
- **Neutrals: tinted.** Pure-gray neutral is the second most common tell.

---

## 13. Redesign Audit Protocol

When asked to improve existing UI, **do not** start by changing
components. Run this protocol first.

1. **Current state analysis.** What is working? What feels generic?
   List the 3, 5 things a user would say "this looks like every other
   app" about.
2. **Dial assessment.** What are the *current* (implicit) dial values?
   Most AI-generated UIs sit at VARIANCE 2, MOTION 2, DENSITY 5, the
   generic default.
3. **Target dial values.** Where should this go? Pick *one* dial to
   push the furthest. Do not move all three.
4. **One change at a time.** Spacing, *then* type, *then* layout. Do
   not rewrite the whole component tree in one pass.
5. **Verify against anti-slop rules.** Section 3 is the merge checklist.
   If the new design still hits two or more tells, iterate.

---

## 14. Decision Shortcuts (30-Second Rule)

When you have 30 seconds, use this unified table.

| Question                  | Default answer                                      |
|---------------------------|-----------------------------------------------------|
| Which font?               | One family, 2 weights, + 1 accent family if editorial |
| Which color?              | 1 primary + 1 accent + tinted neutrals              |
| Which layout?             | Read your VARIANCE dial, then commit                |
| Which motion?             | Read your MOTION dial, then commit                  |
| Centered or left-aligned? | Headlines: either. Body: **always left.**           |
| Card or no card?          | Card only if grouping ≥ 3 related items             |
| Icon next to label?       | Only for the **one** primary action per view        |
| New component variant?    | No, until 3+ use cases exist                        |
| Which easing?             | `cubic-bezier(0.23, 1, 0.32, 1)` (ease-out)         |
| How fast?                 | 200 ms ±50 ms                                       |
| What to animate?          | `transform` + `opacity` only                        |
| Animate at all?           | Only if MOTION dial > 3 and not hundreds/day        |
| Spacing unit?             | 4px or 8px base, scale only, no raw px             |
| Button label?             | Verb first, specific, sentence case                 |
| Error message?            | What happened + what to do                          |
| Touch hover?              | Gate with `@media (hover: hover)`                   |
| Reduced motion?           | Replace with 0.01ms, don't disable                  |

Override with a reason, not a feeling.

---

## 15. Review Checklist (Pre-Merge)

Any "yes" is a fix. Run all four sections.

### 15.1 Typography
- [ ] Typeface has character (not Inter/Roboto/Arial/system).
- [ ] At least 3 hierarchy levels with measurable size *and* weight diff.
- [ ] Body `line-height` is a multiple of the base spacing unit.
- [ ] Heading sizes come from a modular scale.
- [ ] Body line length is `45, 75ch`.

### 15.2 Color
- [ ] No pure `#000`/`#fff` surfaces; neutrals are tinted.
- [ ] Accent used in 1, 2 states only.
- [ ] Body text passes WCAG AA (AAA preferred).
- [ ] Dark mode is a separate palette, not an invert.
- [ ] State never conveyed by color alone.

### 15.3 Spacing & Layout
- [ ] All spacing values from a `--space-*` scale.
- [ ] No raw pixel values for `margin`, `padding`, or `gap`.
- [ ] At least one intentional grid break (asymmetry).
- [ ] Max content width set (`~70ch` for body).
- [ ] No card-in-card nesting.
- [ ] No identical icon-heading-paragraph grids (3+ repeats).
- [ ] No center-aligned body text outside of hero/ceremony.

### 15.4 Motion & Accessibility
- [ ] `transition: all` not used; specify `transform, opacity`.
- [ ] No `scale(0)` entry; start from `scale(0.95)` + `opacity: 0`.
- [ ] No `ease-in` on UI; use `ease-out` or custom curve.
- [ ] Popover `transform-origin` set via trigger rect, not `center`.
- [ ] No motion on `Enter`/`Space` keyboard activation.
- [ ] Micro-interaction duration ≤ 300 ms.
- [ ] Hover styles wrapped in `@media (hover: hover)`.
- [ ] Rapidly-clicked elements use `transition`, not `@keyframes`.
- [ ] Exit is 20, 30% faster than entry.
- [ ] Not animating `width`, `height`, `top`, `padding`.
- [ ] `prefers-reduced-motion` fallback present.
- [ ] All interactive elements keyboard reachable; focus states visible.
- [ ] Text resizes to 200% without horizontal scroll.

---

## 16. When To Use What

This skill covers all three layers. For 90% of frontend work it is
enough. It consolidates the former three-skill split
(`taste-frontend` dials, `frontend-polish` typo/color/copy,
`design-engineering` motion/components/perf); those names appear in
older docs but resolve to this file.
