# 📊 Real-Time Web Dashboard Technical Architecture & Specification

## 1. Overview

The **Real-Time Web Dashboard** in `agy-tools` provides interactive, browser-based analytics and real-time visualization for token consumption, API costs, prompt cache efficiency, model attributions, and rate limit quotas across Antigravity CLI sessions.

Built with a **zero-dependency** philosophy (pure Node.js standard libraries: `http`, `fs`, `path`, `net`), the dashboard is lightweight, self-refreshing, and operates across two seamless transport modes.

```mermaid
flowchart TD
    subgraph Antigravity CLI & Statusline
        HOOK[Statusline Evaluation\n--hook --write-dashboard] -->|Incremental Sync| CM[Cache Manager]
        CM -->|Builds Payload| PAYLOAD[DashboardPayload\n~/.gemini/antigravity-dashboard/dashboard-data.json]
        HOOK -->|OSC 8 Hyperlink| TERM[Terminal Statusline Badge\n📊 Dashboard]
    end

    subgraph Server Subsystem
        SRV[Local SSE Server\n127.0.0.1:8787\n(Auto-increment on conflict)]
        SRV -->|GET /| HTML_FILE[Serve dashboard.html]
        SRV -->|GET /events| SSE[Server-Sent Events Stream\n5s re-aggregation]
        SRV -->|GET /data.json| JSON_DATA[Serve JSON Payload]
        SRV -->|GET /dashboard-data.js| JS_DATA[Serve JSONP / Script Payload]
    end

    subgraph Browser Client
        CLIENT[dashboard.html Client UI]
        CLIENT -->|Transport A: http://| SSE_REC[SSE Listener -> Live DOM & SVG Update]
        CLIENT -->|Transport B: file://| POLL_REC[Script-Tag Polling -> Fallback DOM Update]
        CLIENT --> SVG[Pure SVG Dynamic Charts]
        CLIENT --> FILTERS[Client-side Date & Model Filters]
    end

    PAYLOAD -.->|Reads| SRV
    TERM -.->|Click Opens Browser| CLIENT
```

---

## 2. Server Architecture & Network Security

### Local Loopback Security Constraint
Token usage logs, conversation metadata, and workspace paths are private developer data.
- **Loopback Binding ONLY**: The server strictly binds to `127.0.0.1` (`localhost`). It **NEVER** binds to `0.0.0.0` or public network interfaces.
- **CORS Headers**: Sends `Access-Control-Allow-Origin: *` to permit `file://` local browser contexts (origin `null`) to fetch data from localhost.

### Port Management & Conflict Resolution
- **Default Port**: `8787`.
- **EADDRINUSE Auto-Increment**: If port 8787 is occupied by another process, the server automatically tries up to 10 sequential ports (`8788`, `8789`, ... `8797`).
- **Random Port Allocation**: Running with `--port 0` binds to a free OS-assigned ephemeral port.
- **Port File Registration**: When bound, the server atomically writes `{ port, pid, startedAt, payloadVersion }` to `~/.gemini/antigravity-dashboard/dashboard-server.json`. When shutting down cleanly, it unlinks the file.

### Self-Termination & Staleness Watchdog
- **Code Staleness Watchdog**: The server periodically checks modification times of files in `src/*.js`. If code has been updated on disk while the server is running, it gracefully terminates so a fresh instance is auto-respawned by subsequent statusline evaluations.

---

## 3. Dual Transport Architecture

The dashboard supports two independent data delivery transports:

```
                          ┌───────────────────────────────────────────────┐
                          │         Browser: dashboard.html               │
                          └───────┬───────────────────────────────┬───────┘
                                  │                               │
                      [Attempt SSE Connection]                    │
                                  │                               │
                       ┌──────────┴──────────┐                    │
                   Success                 Failure                │
                       │                       │                  │
                       ▼                       ▼                  ▼
               [HTTP SSE Transport]     [Fallback: Script-Tag Injection Polling]
             - Real-time push (/events) - Periodic <script src="dashboard-data.js?t=...">
             - Latency: ~50ms           - No server required (pure file://)
```

### Transport 1: Real-Time SSE Stream (`http://`)
- **Route**: `GET /events`.
- **Header**: `Content-Type: text/event-stream; Cache-Control: no-store`.
- **Push Interval**: Pushes an updated `DashboardPayload` every 5 seconds (or immediately on turn completion).
- **Auto-Reconnect**: Browser `EventSource` automatically reconnects if the stream drops.

### Transport 2: Offline Script-Tag Injection Polling (`file://`)
- **Challenge**: Modern browsers block `fetch()` and `XMLHttpRequest` when opening local files (`file:///.../dashboard.html`) due to strict CORS rules on origin `null`.
- **Solution**: The page dynamically appends a timestamped `<script src="dashboard-data.js?t=<timestamp>">` element to `<head>`.
- `dashboard-data.js` executes `window.__setDashboardData(<payload>)`, updating the dashboard state without requiring an active HTTP server.

### Auto-Upgrade Mechanism
When `dashboard.html` loads from a `file://` URL:
1. It immediately renders the embedded baseline payload.
2. It attempts to connect to `http://127.0.0.1:8787/events`.
3. If the SSE handshake succeeds, it switches to **Live SSE Mode** (glowing green badge).
4. If connection fails, it maintains **Script Polling Mode** (orange polling badge) at the configured interval (`--refresh <sec>`, default: 5s).

---

## 4. Visual Components & SVG Chart Engine

The client UI is implemented as a single, self-contained HTML file with embedded CSS and JavaScript—no external CDN dependencies, npm packages, or third-party chart libraries.

### Dynamic SVG Vector Chart
- **Dynamic Y-Axis Scaling**: Computes optimal tick intervals and gridlines based on maximum token volume in the selected date window.
- **Stacked Visual Bars**:
  - **Input Tokens** (Base prompt)
  - **Cached Tokens** (Prompt cache hit segment, displayed in distinctive green)
  - **Output Tokens** (Generation segment, displayed in bright amber)
- **Interactive Tooltips**: Hovering over any daily bar displays a floating tooltip with exact input, cached, output, hit rate %, session count, and cost.
- **Zero Pixelation**: Renders crisp vector graphics on high-DPI (Retina / 4K) displays.

### Executive KPI Summary Cards
- **Today**: Tokens, cost, and cache savings.
- **Yesterday**: 24h comparative baseline.
- **7-Day Total & 30-Day Total**: Aggregated metrics across weekly and monthly horizons.
- **Cache Hit Rate**: Visual circular indicator showing prompt caching efficiency.

### Multi-Model Attribution & Reasoning Breakdown
- **Model Usage Breakdown Table**: Displays exact token volume, cost, and session counts partitioned by model (e.g., `gemini-3.7-flash`, `gemini-3.7-flash-thinking`, `claude-3.7-sonnet`, `claude-3-opus`).
- **Reasoning Effort Tracking**: Distinguishes between standard generation and thinking/reasoning turns.

### Interactive Client-Side Filters
- **Date Filters**: Preset buttons for *Today*, *Yesterday*, *Last 7 Days*, *Last 30 Days*, and *All-Time*, plus a custom date range picker (`YYYY-MM-DD..YYYY-MM-DD`).
- **Model Filter**: Dropdown menu to isolate metrics for a single model or view aggregate totals.

### Rate Limits & Live Quota Visualizer
- Visual 5-hour and 7-day progress bars reflecting live Language Server quotas with live reset countdown timers.

---

## 5. Statusline OSC 8 Hyperlink & VS Code Integration

### The OSC 8 Hyperlink Protocol
The statusline badge rendered by `agy-tokens --hook` appends a clickable terminal hyperlink using ANSI OSC 8 escape sequences:
```
\x1b]8;;http://127.0.0.1:8787/\x07📊 Dashboard\x1b]8;;\x07
```
In supported terminals (Windows Terminal, iTerm2, Kitty, Alacritty, WezTerm), clicking `📊 Dashboard` opens the browser directly.

### VS Code Terminal Quirk & Auto-Spawn Integration
- **The Issue**: By default, VS Code's integrated terminal intercepts `file://` hyperlinks and opens them as raw text in the code editor rather than launching the default browser.
- **The Solution (`dashboard-link.js`)**:
  1. Detects VS Code environment via `TERM_PROGRAM === 'vscode'`.
  2. Resolves target as `http://127.0.0.1:8787/`.
  3. Probes the port; if down, writes a spawn intent and spawns a detached background server process (`node bin/agy-tokens.js --serve --port 8787`) in ~1ms without blocking the statusline.
  4. Subsequent clicks in VS Code launch the browser instantly.

---

## 6. Artifact Storage Layout

All dashboard artifacts are written atomically to `~/.gemini/antigravity-dashboard/`:

```
~/.gemini/antigravity-dashboard/
├── dashboard.html          # Self-contained HTML/CSS/JS single-page application
├── dashboard-data.js       # JSONP payload for file:// script-tag polling
├── dashboard-data.json     # Raw JSON payload for HTTP endpoints and API consumers
└── dashboard-server.json   # Active server lock/port registry
```

---

## 7. CLI Commands Reference

```bash
# Generate static HTML dashboard and open in default browser
agy-tokens --html --open

# Start real-time SSE dashboard server on default port 8787
agy-tokens --serve --open

# Start server on a specific custom port
agy-tokens --serve --port 9090

# Start server on a random available port
agy-tokens --serve --port 0

# Set custom polling refresh interval (e.g. 2 seconds)
agy-tokens --html --refresh 2
```
