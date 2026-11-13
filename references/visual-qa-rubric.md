# Visual QA Rubric & Defect Remediation Recipes

This reference defines the severity levels, defect criteria, and concrete code remediation recipes for visual defects identified across SVG, CSS, and Canvas assets.

---

## 1. Severity Classification Matrix

| Severity | Definition | Examples | Action Required |
| :--- | :--- | :--- | :--- |
| 🔴 **CRITICAL** (Blocker) | Visual failure that renders content illegible, completely misaligned, or structurally broken. | • Text clipped by bounding box.<br>• Elements overlapping illegibly.<br>• Leftover "Lorem ipsum" in production.<br>• Contrast ratio < 3.0:1. | Must fix immediately before output delivery. |
| 🟠 **MAJOR** (Degradation) | Visual flaw that violates accessibility standards or layout stability. | • Contrast ratio between 4.5:1 and 7.0:1 for normal text.<br>• Connector lines crossing through node labels.<br>• Missing padding causing cramped appearance. | Must remediate before quality sign-off. |
| 🟡 **MINOR** (Polish) | Aesthetic inconsistencies that do not break comprehension or accessibility. | • Inconsistent corner radii (e.g. mix of 4px and 12px).<br>• Stroke width variation on parallel connectors.<br>• Sub-optimal baseline alignment by 1-2px. | Refine if time permits. |

---

## 2. Defect Remediation Recipes

### Recipe 1: Text Clipping & Descender Truncation
#### Defect Example (Broken)
```xml
<!-- Descenders in 'Deploying' or 'Processing' get cut off -->
<rect x="50" y="50" width="120" height="24" rx="4" fill="#F0FDFA"/>
<text x="60" y="68" font-size="13" font-family="sans-serif" fill="#115E59">Deploying...</text>
```

#### Remediated Example (Fixed)
```xml
<!-- Fixed: Increased height to 36px, centered with dominant-baseline -->
<rect x="50" y="50" width="130" height="36" rx="8" fill="#F0FDFA" stroke="#0D9488" stroke-width="1.5"/>
<text x="115" y="68" font-size="13" font-family="sans-serif" font-weight="600" fill="#115E59" 
      text-anchor="middle" dominant-baseline="central">Deploying...</text>
```

---

### Recipe 2: z-Index Collision & Line Stacking
#### Defect Example (Broken)
```xml
<!-- The connector path is declared AFTER the node rect and text, drawing across it -->
<rect x="100" y="100" width="140" height="40" fill="#FAF5FF" stroke="#9333EA"/>
<text x="170" y="120" text-anchor="middle" dominant-baseline="central">Worker Node</text>
<path d="M 50 120 L 300 120" stroke="#6B21A8" stroke-width="2"/>
```

#### Remediated Example (Fixed)
```xml
<!-- Fixed: Connectors grouped and painted BEFORE nodes and text -->
<g id="connectors">
  <path d="M 50 120 L 300 120" stroke="#6B21A8" stroke-width="2" stroke-dasharray="4 2"/>
</g>
<g id="nodes">
  <rect x="100" y="100" width="140" height="40" rx="8" fill="#FAF5FF" stroke="#9333EA" stroke-width="1.5"/>
  <text x="170" y="120" font-size="13" font-family="sans-serif" font-weight="600" fill="#6B21A8"
        text-anchor="middle" dominant-baseline="central">Worker Node</text>
</g>
```

---

### Recipe 3: Placeholder Detection & Automated Remediation
Use this Node.js scanning routine during QA checks:

```javascript
function auditPlaceholders(sourceText) {
  const patterns = [
    /\b(lorem\s+ipsum)\b/i,
    /\b(sample\s+text)\b/i,
    /\b(todo:?)\b/i,
    /\b(untitled)\b/i,
    /\b(placeholder)\b/i,
    /\b(foo\s*bar)\b/i
  ];
  
  const defects = [];
  const lines = sourceText.split('\n');
  lines.forEach((line, idx) => {
    patterns.forEach(regex => {
      const match = line.match(regex);
      if (match) {
        defects.push({
          line: idx + 1,
          term: match[0],
          content: line.trim()
        });
      }
    });
  });
  return defects;
}
```

---

### Recipe 4: WCAG AAA Luminance & Contrast Calculation
Formula implemented in pure JavaScript for validating color tokens:

```javascript
function getLuminance(hex) {
  const cleanHex = hex.replace('#', '');
  const r = parseInt(cleanHex.substring(0, 2), 16) / 255;
  const g = parseInt(cleanHex.substring(2, 4), 16) / 255;
  const b = parseInt(cleanHex.substring(4, 6), 16) / 255;

  const toLinear = (c) => (c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4));
  return 0.2126 * toLinear(r) + 0.7152 * toLinear(g) + 0.0722 * toLinear(b);
}

function getContrastRatio(fgHex, bgHex) {
  const l1 = getLuminance(fgHex);
  const l2 = getLuminance(bgHex);
  const lighter = Math.max(l1, l2);
  const darker = Math.min(l1, l2);
  return (lighter + 0.05) / (darker + 0.05);
}

// Example:
// Teal 800 (#115E59) on Teal 50 (#F0FDFA) -> Ratio ~ 8.9:1 (WCAG AAA Pass)
```
