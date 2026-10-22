---
name: design-cyberpunk-brainmap
description: Cyberpunk CRT scanline & topology UI with deep observatory radial gradients, 7.5s laser sweep animations, and pure DOM/SVG neural pulse DAG networks.
---

# Cyberpunk Observatory UI & Brainmap Topology Runbook

## 1. Executive Summary & Aesthetic Architecture

The **Cyberpunk Brainmap** design system provides a specialized visual language for complex DAG networks, multi-agent topologies, neural pulse activations, and Git lineage trees.

### Core Visual Principles
1. **The Deep Observatory Background**:
   ```css
   background: radial-gradient(1200px at 44% 96%, #0b2340 0%, #01030a 100%);
   ```
   Provides high depth and cinematic contrast without flat black dullness.
2. **Holographic CRT Scanlines**:
   An overlay pattern giving a retro-futuristic terminal monitor feel:
   ```css
   background-image: repeating-linear-gradient(
     0deg,
     rgba(0, 0, 0, 0.25) 0px,
     rgba(0, 0, 0, 0.25) 1px,
     transparent 1px,
     transparent 2px
   );
   ```
3. **The 7.5s Laser Sweep**:
   A continuous, luminous laser scanner line descending across the interface:
   ```css
   @keyframes sweep {
     0% { top: -10%; opacity: 0; }
     20% { opacity: 0.65; }
     80% { opacity: 0.65; }
     100% { top: 110%; opacity: 0; }
   }
   ```
4. **Zero-Dependency Vector Topology**:
   Renders complex tree networks using standard SVG cubic Bézier curves and HTML DOM elements—never requiring external multi-megabyte D3 or graph libraries.

---

## 2. DAG Topology & Neural Pulse Transmission

### Directed Acyclic Graph (DAG) Structure
A standard multi-agent or Git execution lineage is structured into sequential tiers:
- **Genesis / Root**: Initial user prompt or seed commit.
- **Orchestration Layer**: Master agent decomposition and domain topology.
- **Domain Worker Nodes**: Specialized execution agents operating in parallel.
- **Blind QA Quality Gate**: Adversarial validation and verification node.
- **Production Milestone**: Final verified deployment deliverable.

### Animated Neural Pulses
Neural pulses represent active data packets or messages flowing along edges:

```xml
<!-- Glowing Base Connection -->
<path d="M 120 180 C 220 180, 220 280, 320 280" 
      fill="none" stroke="rgba(0, 240, 255, 0.2)" stroke-width="2"/>

<!-- Animated Energy Pulse -->
<path d="M 120 180 C 220 180, 220 280, 320 280" 
      fill="none" stroke="#00f0ff" stroke-width="3"
      stroke-dasharray="16 140" stroke-dashoffset="0"
      style="animation: pulseTravel 2.8s linear infinite;"/>
```

---

## 3. Interactive Focus & Inertial States

### The 82% Dimming Rule (`.dimmed`)
When a user hovers over a specific node or lineage branch:
- The active branch and connected edges remain fully illuminated (`opacity: 1.0`, glow intensified).
- All unselected nodes, unrelated paths, and background clutter are immediately dimmed to 18% opacity:
  ```css
  .node.dimmed, .edge.dimmed {
    opacity: 0.18;
    filter: grayscale(0.6);
    transition: opacity 180ms cubic-bezier(0.4, 0, 0.2, 1);
  }
  ```

### Monospace Telemetry Cards
Hovering triggers an inertial telemetry card showing:
- Node UUID & Classification
- Execution Duration & Token Burn
- Defect Score & Verification Status
- Upstream / Downstream Link References
