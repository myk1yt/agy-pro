# Designer - Zero-MCP Self-Contained Design Specialist

[![Antigravity Plugin](https://img.shields.io/badge/Antigravity-Plugin-blue.svg)](https://github.com/google/antigravity)
[![Zero-MCP](https://img.shields.io/badge/Zero--MCP-Web--Standards-brightgreen.svg)](#core-philosophy)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey.svg)](#quick-start-one-click-installation)

An autonomous multi-agent design specialist and visual QA engineer for Google Antigravity. Built strictly on zero-MCP web standards (HTML5, CSS3, SVG, WebGL/GLSL Canvas), Designer delivers mathematically verified UI/UX designs, interactive sandbox widgets, and 3D graphics directly inside chat cards without requiring external design tools, Figma plugins, or Blender daemons.

---

## 🚀 Quick Start (One-Click Installation)

Open your terminal in the repository root directory and run the single command matching your operating system:

### Windows (PowerShell)
```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-designer.ps1
```

> 💡 **Beginner Tip (초보자 / 컴맹을 위한 팁)**:
> In Windows File Explorer, press **Shift + Right-Click** in an empty area inside the repository folder and select **"Open PowerShell window here"** or **"Open in Terminal"**, then copy and paste the command above and press Enter.

### macOS / Linux (Terminal)
```bash
bash scripts/install-designer.sh
```

> **What the installer does automatically**:
> 1. Registers the `designer` plugin with Antigravity CLI via `agy plugin install`.
> 2. Deploys all 5 modular design skills to `~/.gemini/config/skills`.
> 3. Verifies agent registration via `agy agents`.

---

## 💡 How to Use in Antigravity

Once installed, you can invoke Designer inside Antigravity CLI in two easy ways:

### Method 1: Using the `/agent` Menu

Type `/agent` in the Antigravity prompt and select `designer` using arrow keys:

```text
┌────────────────────────────────────────────────────────┐
│ Select an Agent                                        │
├────────────────────────────────────────────────────────┤
│ > designer          (Zero-MCP Design Specialist)       │
│   security-reviewer (Enterprise Multi-Agent Audit)     │
│   agy_help          (Antigravity Ecosystem Guide)      │
└────────────────────────────────────────────────────────┘
  ▲/▼: Navigate   Enter: Select   Esc: Cancel
```

### Method 2: Direct Mention (`@designer`)

Mention `@designer` anywhere in your prompt:

#### 1. Mathematical Inline Vector SVG Diagram
```text
@designer Create an architectural flowchart for an event-driven microservice system with API Gateway, Kafka bus, and 3 worker nodes.
```
*Outputs a mathematically balanced 680px SVG with clean contrast, zero text clipping, and semantic color matrix.*

#### 2. Interactive In-Chat Sandbox (Sci-Widget)
```text
@designer Build an interactive sci-widget showing real-time PID controller tuning with sliders for P, I, D parameters and an animated canvas response graph.
```
*Outputs a self-contained HTML/CSS/JS widget running at 60fps with real-time controls inside the Antigravity preview.*

#### 3. Web-Native 3D Shader Canvas
```text
@designer Generate a procedural 3D rotating wireframe dodecahedron with a GLSL particle wave field in pure WebGL.
```
*Outputs lightweight, zero-dependency WebGL/Canvas code that runs directly in modern browsers.*

---

## 📦 Bundled Modular Skills

Designer comes bundled with 5 specialized design skills deployed globally:

| Skill | Description |
|---|---|
| **`design-core-harness`** | Visual QA verification loop, mandatory pre-contract `design.md` protocol, and 4-point defect checklist. |
| **`design-vector-svg`** | Fixed 680px coordinate SVG layout engine, character width formulas, and 9-family color matrix. |
| **`design-interactive-sandbox`** | Live in-chat interactive HTML widgets (sci-widgets) with parameter controls and 60fps canvas. |
| **`design-3d-canvas`** | Procedural 3D geometry and GLSL vertex/fragment shaders without external dependencies. |
| **`design-cyberpunk-brainmap`** | Cyberpunk CRT scanline & observatory topology UI with DOM/SVG neural pulse DAG networks. |

---

## 🗑️ One-Click Uninstallation

To cleanly remove the Designer plugin and all associated skills:

### Windows (PowerShell)
```powershell
powershell -ExecutionPolicy Bypass -File scripts/uninstall-designer.ps1
```

### macOS / Linux (Terminal)
```bash
bash scripts/uninstall-designer.sh
```

---

## 🛡️ License

MIT © Google Antigravity Team & Contributors
