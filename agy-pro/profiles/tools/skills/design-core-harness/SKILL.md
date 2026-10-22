---
name: design-core-harness
description: Visual QA verification loop, pre-contract design.md protocol, 4-dimension defect checklist, and snapshot inspection workflows for Zero-MCP design generation.
---

# Design Core Harness Runbook

## 1. Executive Summary & Core Philosophy
The **Design Core Harness** provides the foundational quality assurance and pre-contract governance for all visual artifacts generated within the Antigravity ecosystem.

### The Fundamental Axiom
> *"These outputs almost never come out right on the first try, and you cannot judge them from code alone. You must inspect the actual result against the specification, diff it, fix, and verify again. 'No console errors, HTTP 200' is the floor, not the finish line."*

Every visual asset—whether an inline SVG diagram, an interactive `sci-widget`, a WebGL shader canvas, or a cyberpunk topology—must pass through this harness.

---

## 2. Mandatory `design.md` Pre-Contract Protocol

Before generating production code for any complex design, the agent must define or reference a `design.md` pre-contract. This pre-contract guarantees mathematical consistency and prevents layout collisions before rendering begins.

### `design.md` Schema Contract
```markdown
# Design Specification Pre-Contract

## 1. Scope & Viewport Dimensions
- **Canvas / Viewport**: e.g., 680px fixed width (viewBox="0 0 680 420")
- **Aspect Ratio**: [1:1 | 16:9 | 4:3 | Custom]
- **Target Container**: [Antigravity Chat | VS Code Markdown | Browser Canvas]

## 2. Color Palette Selection (Max 3 Families)
- **Primary Family**: [Family Name, e.g., Teal] (50: #..., 200: #..., 600: #..., 800: #...)
- **Secondary Family**: [Family Name, e.g., Blue] (50: #..., 200: #..., 600: #..., 800: #...)
- **Accent Family**: [Family Name, e.g., Amber] (50: #..., 200: #..., 600: #..., 800: #...)
- **Contrast Verification**: Verified WCAG AAA ratio >= 7:1 for all text elements.

## 3. Typography & Bounding Box Budget
- **Base Font**: System Sans (Inter, -apple-system, Segoe UI, Roboto)
- **Node Width Calculation**:
  - Latin: chars * 6.5px + 20px padding
  - CJK/Hangul: chars * 13px + 20px padding
- **Node Count**: <= 15 nodes total

## 4. Interactive & State Behaviors
- **Default State**: Initial idle presentation
- **Hover / Focus**: 180ms cubic-bezier transition, inactive element 82% dimming (.dimmed)
- **Animation Loop**: 60fps delta-time normalized, throttled resize handling
```

---

## 3. The 4-Dimension Defect Checklist

Every visual artifact must undergo inspection across four distinct quality dimensions:

### Dimension 1: Ascender / Descender Clipping
- **Failure Mode**: Lowercase tails (`g, j, p, q, y`) or uppercase accents (`Å, Ê, Ö`) clipped by bounding container edges.
- **Root Cause**: Tight container heights without baseline metrics, or `overflow: hidden` without padding.
- **Acceptance Rule**:
  - In SVG: Use `dominant-baseline="central"` or `dominant-baseline="middle"` with explicit `text-anchor`.
  - In HTML/CSS: Minimum line-height of `1.4` to `1.5`, with minimum vertical padding $\ge 8\text{px}$.

### Dimension 2: z-Index Collision & Layout Overlap
- **Failure Mode**: Connector lines rendering on top of node labels, overlapping card margins, or z-index stacking context confusion.
- **Root Cause**: Improper DOM/SVG tree ordering or arbitrary `z-index: 9999` hacks.
- **Acceptance Rule**:
  - In SVG: Strictly order element groups:
    1. `<defs>` (gradients, markers, filters)
    2. Background rectangles
    3. Connector lines and path arrows
    4. Node cards and surface rectangles
    5. Text labels and icon glyphs
    6. Badges and top-level overlays
  - In CSS: Enforce isolated stacking contexts using `isolation: isolate` on card components.

### Dimension 3: Placeholder Text Retention
- **Failure Mode**: Remnants of placeholder text, debug text, or dummy data left in deliverable.
- **Root Cause**: Incomplete template replacement.
- **Acceptance Rule**:
  - Zero tolerance for: `"Title"`, `"Subtitle"`, `"Lorem ipsum"`, `"TODO"`, `"Sample"`, `"Placeholder"`, `"Node 1"`, `"Label"`, `"Test"`.
  - Automated regex validation:
    ```javascript
    const PLACEHOLDER_REGEX = /\b(lorem\s+ipsum|placeholder|sample\s+text|todo|untitled|dummy|test\s+label)\b/i;
    ```

### Dimension 4: Figure-Ground Contrast (WCAG AAA)
- **Failure Mode**: Low-contrast gray text on light-gray cards, or dark-blue text on dark backgrounds.
- **Root Cause**: Failure to adhere to calibrated color tiers.
- **Acceptance Rule**:
  - **Normal Text (<18px regular or <14px bold)**: Contrast ratio $\ge 7:1$ (WCAG AAA).
  - **Large Text ($\ge 18\text{px}$ regular or $\ge 14\text{px}$ bold)**: Contrast ratio $\ge 4.5:1$ (WCAG AAA).
  - Use relative luminance formula:
    $$L = 0.2126 \times R + 0.7152 \times G + 0.0722 \times B$$
    $$\text{Contrast Ratio} = \frac{L_1 + 0.05}{L_2 + 0.05}$$
  - Standardized Tier Pairing:
    - Tier 50 Background $\to$ Tier 800 Text (Contrast ratio $> 8.5:1$, PASS).
    - Tier 900 Dark Background $\to$ Tier 50/100 Light Text (Contrast ratio $> 12:1$, PASS).

---

## 4. Snapshot Inspection Protocol

When headless browser execution or automated test runners are available:
1. **Render**: Mount artifact in isolated browser context or render SVG directly.
2. **Measure**: Extract bounding client rects of all text nodes and compare with container boxes.
3. **Capture**: Capture PNG viewport snapshot at $1\times$ and $2\times$ DPR.
4. **Audit**: Run automated pixel contrast verification and overlap detection.
5. **Remediate**: If any of the 4 dimensions fail, apply targeted fixes and re-verify before final sign-off.
