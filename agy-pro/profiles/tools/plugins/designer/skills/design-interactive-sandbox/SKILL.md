---
name: design-interactive-sandbox
description: In-chat live interactive HTML sandbox (sci-widget) for zero-dependency real-time parameter controls, simulations, and 60fps canvas widgets.
---

# In-Chat Live Interactive Sandbox (`sci-widget`) Runbook

## 1. Executive Summary & The `sci-widget` Contract

When a user asks to explore, simulate, or interactively manipulate mathematical, physical, or algorithmic parameters (e.g. *"let me drag a slider and watch it change"*, *"interactive demo"*), the Designer agent does not return a static image. Instead, it outputs a self-contained, live mini-application enclosed in a ` ```sci-widget ` code block.

### The In-Chat Sandbox Contract
1. **Fenced Code Block**: Code must be wrapped in ` ```sci-widget ... ``` ` within chat responses.
2. **Zero External Dependencies**: Absolute prohibition against external CDN scripts (`<script src="...">`), Google fonts, or external stylesheet links. The HTML document must be 100% self-contained.
3. **Vanilla Web Standards**: Pure HTML5, modern CSS3 (Flexbox/Grid, CSS custom properties), and vanilla ES6+ JavaScript.
4. **Interactive Controls**: Sliders (`<input type="range">`), toggle buttons, color palette pickers, and live value readouts.
5. **High-Performance Canvas Loop**: 60fps canvas rendering utilizing `requestAnimationFrame` with delta-time normalization.

---

## 2. Widget Architecture & DOM Layout

Every `sci-widget` document follows a clean 3-part layout:

```text
┌─────────────────────────────────────────────────────────────┐
│                      Header & Title Bar                     │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│                    Interactive 2D Canvas                    │
│                 (60fps Simulation / Graph)                  │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│                 Control Panel & Sliders                     │
│  [Param 1: ═══●════ 42]   [Param 2: ══════● 85]             │
│  [Play / Pause]   [Reset]   [Preset A]   [Preset B]         │
├─────────────────────────────────────────────────────────────┤
│                 Live Telemetry & Diagnostics                │
│  FPS: 60.0  |  Particles: 120  |  Energy: 1.42 J            │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. High-DPI Canvas & Animation Loop Best Practices

### High-DPI Scaling (Retina Display Support)
To prevent blurry canvas rendering on high-density screens:

```javascript
function resizeCanvas(canvas, ctx) {
  const dpr = window.devicePixelRatio || 1;
  const rect = canvas.getBoundingClientRect();
  canvas.width = Math.floor(rect.width * dpr);
  canvas.height = Math.floor(rect.height * dpr);
  ctx.scale(dpr, dpr);
}
```

### Delta-Time Normalized 60fps Animation Loop
```javascript
let lastTime = performance.now();
let animationFrameId = null;
let isRunning = true;

function loop(currentTime) {
  if (!isRunning) return;
  
  const dt = Math.min((currentTime - lastTime) / 1000, 0.1); // Clamp to prevent spiral of death
  lastTime = currentTime;

  updateSimulation(dt);
  renderCanvas();

  animationFrameId = requestAnimationFrame(loop);
}

// Start loop
animationFrameId = requestAnimationFrame(loop);
```

### Resource Cleanup
To prevent background CPU drain when a widget is closed or refreshed:
- Provide explicit `pause()` / `destroy()` routines.
- Cancel `animationFrameId` via `cancelAnimationFrame(animationFrameId)`.
- Disconnect any `ResizeObserver` instances.

---

## 4. UI/UX Guidelines for In-Chat Controls

1. **Input Sliders (`<input type="range">`)**:
   - Always pair each slider with a live companion label (`<span id="val-frequency">2.5 Hz</span>`).
   - Update the label immediately in the `input` event listener, not just on `change`.
2. **Tactile Feedback**:
   - Use subtle `:hover` and `:active` states on buttons (`transform: translateY(-1px)`).
   - Use CSS custom properties for cohesive themes.
3. **Readable Dark Theme**:
   - Default to a dark theme (`#0d1117` or `#0b0f19`) to match developer terminals and reduce glare.
   - Use high-contrast accents (Cyan `#00f0ff`, Green `#10b981`, Amber `#f59e0b`).
