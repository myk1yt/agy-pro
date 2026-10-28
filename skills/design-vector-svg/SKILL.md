---
name: design-vector-svg
description: Mathematically precise 680px inline SVG layout engine with character width measurement formulas, 9-family 4-tier color matrix, and complexity budgets.
---

# Mathematical Inline Vector SVG Layout Engine

## 1. Executive Summary & Viewport Specification

This skill governs the production of mathematically consistent, high-legibility inline SVG diagrams.

### The 680px Viewport Invariant
All inline SVG diagrams authored by the Designer agent MUST declare:

```xml
<svg viewBox="0 0 680 [height]" width="100%" xmlns="http://www.w3.org/2000/svg">
```

### Why 680px?
- **Chat Native**: Standard viewport width for Antigravity conversation message cards.
- **Responsive Scaling**: `width="100%"` allows seamless scaling on mobile screens while preserving exact coordinate placement.
- **Predictable Typography**: At 680px width, body text at 13px–14px renders with crisp readability without subpixel font fuzziness.

---

## 2. Character Width Calculation Formulas

Never eyeball node widths or use static guesses. Text overflowing or colliding with bounding box borders is a critical visual defect.

### Character Width Metrics (at Base Font Size 13px)
| Character Category | Glyphs / Script | Width Per Character |
| :--- | :--- | :--- |
| **Latin Lowercase & Digits** | `a-z`, `0-9`, symbols | `6.2px – 6.5px` (Standard: `6.5px`) |
| **Latin Uppercase & Wide** | `A-Z`, `@`, `%`, `&` | `7.2px – 7.8px` (Standard: `7.5px`) |
| **CJK / Korean Hangul** | Korean (한글), Chinese, Kanji | `13.0px` (Strict Em square) |
| **Spaces & Thin Punctuation**| ` `, `.`, `,`, `:`, `;`, `!` | `3.5px – 4.0px` |

### Node Box Sizing Formula
To determine the bounding box width for any label:

$$\text{TextWidth} = (N_{\text{latin}} \times 6.5) + (N_{\text{cjk}} \times 13.0)$$

$$\text{BoxWidth} = \text{TextWidth} + 2 \times \text{Padding}_{x}$$

- **Mandatory Minimum Horizontal Padding**: $\text{Padding}_{x} \ge 10\text{px}$ on each side ($\text{Total Padding} \ge 20\text{px}$).
- **Recommended Default Horizontal Padding**: $16\text{px}$ on each side ($\text{Total Padding} = 32\text{px}$).
- **Minimum Box Height**:
  - Single-line node: $36\text{px}$ minimum.
  - Multi-line card (title + subtitle): $56\text{px} – 64\text{px}$.
- **Vertical Alignment**:
  - Use `dominant-baseline="central"` and `text-anchor="middle"` on `<text>` elements positioned at $Y_{\text{box}} + (\text{Height} / 2)$.

---

## 3. 9-Family 4-Tier Color Matrix

To maintain high contrast and eliminate cognitive fatigue, diagrams use 9 standardized color families structured across 4 calibrated tiers:

| Family | Tier 50 (Background) | Tier 200 (Border / Line) | Tier 600 (Stroke / Accent) | Tier 800 (Text WCAG AAA) |
| :--- | :--- | :--- | :--- | :--- |
| **Teal** | `#F0FDFA` | `#99F6E4` | `#0D9488` | `#115E59` |
| **Purple** | `#FAF5FF` | `#E9D5FF` | `#9333EA` | `#6B21A8` |
| **Coral** | `#FFF7ED` | `#FED7AA` | `#EA580C` | `#7C2D12` |
| **Blue** | `#EFF6FF` | `#BFDBFE` | `#2563EB` | `#1E40AF` |
| **Green** | `#F0FDF4` | `#BBF7D0` | `#16A34A` | `#14532D` |
| **Amber** | `#FFFBEB` | `#FDE68A` | `#D97706` | `#78350F` |
| **Red** | `#FEF2F2` | `#FECACA` | `#DC2626` | `#991B1B` |
| **Pink** | `#FDF2F8` | `#FBCFE8` | `#DB2777` | `#9D174D` |
| **Gray** | `#F8FAFC` | `#E2E8F0` | `#475569` | `#0F172A` |

*Note: For Coral, Green, and Amber, Tier 900 values (`#7C2D12`, `#14532D`, `#78350F`) are specified as Tier 800 text tokens to guarantee strict WCAG AAA contrast $> 8.5:1$ against Tier 50 backgrounds.*

### Dark-Mode Inversion Pattern
When rendering dark-mode diagrams:
- **Canvas Base**: `#090D16` or `#0F172A`
- **Node Fills**: Tier 900 / 950 (`#1E293B`)
- **Node Borders**: Tier 600
- **Text Labels**: Tier 50 (`#F8FAFC`) or Tier 100 (`#F1F5F9`)

---

## 4. Complexity Budget Constraints

To prevent visually cluttered or unreadable diagrams, adhere strictly to these complexity limits:

1. **Node Limit**: Maximum **15 semantic nodes** per diagram. Split larger systems into sequential sub-diagrams.
2. **Color Family Limit**: Maximum **3 color families** per diagram:
   - **Primary Family** (e.g. Blue): Main system components and standard workflow.
   - **Secondary Family** (e.g. Purple): Ingestion, orchestration, or auxiliary workers.
   - **Accent Family** (e.g. Teal or Amber): Verification gates, success milestones, or alerts.
3. **Connector Discipline**:
   - Use orthogonal (horizontal/vertical) lines or smooth cubic Bézier curves (`M x1 y1 C cx1 cy1 cx2 cy2 x2 y2`).
   - Standard stroke width: `2px`.
   - Arrowhead markers defined once in `<defs>` with `orient="auto"`.

---

## 5. Standard SVG Defs & Arrow Marker Template

Always declare your markers in `<defs>` at the top of the SVG:

```xml
<defs>
  <!-- Standard Arrow Marker -->
  <marker id="arrow-blue" viewBox="0 0 10 10" refX="6" refY="5" 
          markerWidth="6" markerHeight="6" orient="auto-start-reverse">
    <path d="M 0 1 L 10 5 L 0 9 z" fill="#2563EB"/>
  </marker>

  <!-- Drop Shadow Filter -->
  <filter id="card-shadow" x="-5%" y="-5%" width="110%" height="115%" filterUnits="userSpaceOnUse">
    <feDropShadow dx="0" dy="2" stdDeviation="3" flood-color="#0F172A" flood-opacity="0.06"/>
  </filter>
</defs>
```
