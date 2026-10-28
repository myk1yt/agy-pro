# ⚡ Gemini Quota Pool Technical Architecture & Specification

## 1. Overview

The **Gemini Quota Pool** subsystem in `agy-tools` provides real-time, 1:1 synchronization with the native Gemini token rate limit and quota pools tracked by the **Antigravity Language Server**.

Antigravity CLI communicates with local language server instances that track rolling window quotas enforced by Google's backend. `agy-tools` discovers this server, connects via HTTPS/HTTP RPC, extracts quota metrics across **5-Hour (5h)** and **7-Day (7d)** sliding windows, and caches them atomically for sub-millisecond statusline rendering.

```mermaid
flowchart TD
    subgraph Antigravity Engine
        LS[Antigravity Language Server\nlanguage_server / agy.exe]
        Q1[(5-Hour Quota Bucket)]
        Q2[(7-Day Quota Bucket)]
        LS --- Q1
        LS --- Q2
    end

    subgraph agy-tools Quota Subsystem
        DISC[Process Discovery\nCIM / WMI / netstat / ps / lsof] -->|Discovers PID & Port| RPC[HTTPS / HTTP RPC Client\nConnect-Protocol-Version: 1]
        RPC -->|/RetrieveUserQuotaSummary\n(Fallback: /GetUserStatus)| LS
        RPC -->|Extracts Fractions & Reset Times| PARSER[Quota Extractor & Countdown Engine]
        PARSER -->|Writes Atomically| CACHE[(~/.gemini/gemini_quota_cache.json\n30s TTL)]
        CACHE -->|Instant <1ms Read| STATUSLINE[Statusline Formatter & Real-Time Badge]
        CACHE -->|Embedded in Payload| DASHBOARD[Real-Time SSE Web Dashboard]
    end
```

---

## 2. Process & Port Discovery

The Language Server process is spawned dynamically by Antigravity CLI and binds to an ephemeral local loopback port. The discovery mechanism works cross-platform:

### Windows Discovery (`win32`)
1. Executes an encoded PowerShell command using `Get-CimInstance Win32_Process` to locate active processes named `language_server`, `agy.exe`, or with command line flags containing `--csrf_token` or `language_server`.
2. Inspects command-line arguments via regex `parseCommandLine`:
   - `--csrf_token=<token>` or `--csrf-token=<token>`: Extracts the authentication CSRF token.
   - `--port=<port>`, `--manager-port=<port>`, or `--api-url=<url>`: Extracts the listening port and protocol (`https` or `http`).
3. If the port is not explicitly present in the command line arguments, queries `netstat -ano -p tcp` matching the detected Process ID (PID) to find all active listening TCP ports on `127.0.0.1`.

### POSIX Discovery (`linux`, `darwin`)
1. Executes `ps -ax -o pid,command` filtering for `language_server` or `agy`.
2. Extracts CSRF token and command line arguments.
3. If necessary, queries `lsof -a -p <PID> -iTCP -sTCP:LISTEN -P -n` to locate the listening port.

---

## 3. HTTPS / HTTP RPC Protocol Specification

The Language Server exposes gRPC-Web / Connect-RPC endpoints over HTTP and HTTPS on `127.0.0.1`.

### RPC Request Headers
```http
POST /exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary HTTP/1.1
Host: 127.0.0.1:<port>
Content-Type: application/json
X-Codeium-Csrf-Token: <csrf_token>
Connect-Protocol-Version: 1
Content-Length: 2

{}
```

### Protocol & Certificate Handling
- **Protocol Priority**: Automatically tries `https` first, then falls back to `http`.
- **TLS Verification**: Local Language Server instances generate self-signed certificates. The client sets `rejectUnauthorized: false` to allow secure local loopback communication without TLS handshake errors.
- **Request Timeout**: Configured with a fast `2500ms` network timeout to prevent any blocking.

---

## 4. Endpoints & Response Schema

### Primary Endpoint: `/RetrieveUserQuotaSummary`
Returns user quota groups, including rolling buckets for Gemini models and third-party models:

```json
{
  "userStatus": {
    "groups": [
      {
        "displayName": "Gemini Models",
        "name": "gemini",
        "buckets": [
          {
            "bucketId": "gemini-5h",
            "displayName": "5-Hour Limit",
            "window": "5h",
            "remainingFraction": 0.792,
            "resetTime": "2026-08-29T11:35:12Z"
          },
          {
            "bucketId": "gemini-weekly",
            "displayName": "Weekly Limit",
            "window": "weekly",
            "remainingFraction": 0.214,
            "resetTime": "2026-09-02T03:00:00Z"
          }
        ]
      }
    ]
  }
}
```

### Fallback Endpoint: `/GetUserStatus`
Used for backward compatibility with older Language Server builds. Parses `clientModelConfigs` / `modelConfigs` to extract `quotaInfo` (`remainingFraction`, `resetTime`).

---

## 5. Quota Buckets & Window Metrics

`agy-tools` extracts two distinct rolling window buckets:

| Window Bucket | Description | Typical Use Case | Statusline Representation |
| :--- | :--- | :--- | :--- |
| **5-Hour (`5h`)** | Short-term burst limit. Replenishes continuously every 5 hours. | Guards against rapid turn-by-turn exhaustion during intense coding sessions. | `5h: ▰▰▰▰▱ 79% (4h 10m)` |
| **7-Day (`7d` / `weekly`)** | Long-term weekly allowance. Replenishes over a 7-day rolling window. | Tracks weekly consumption budget across multiple days. | `7d: ▰▱▱▱▱ 21% (3d 20h)` |

### Metric Calculation
- **Remaining Percentage**: `remainPercent = Math.round(remainingFraction * 100)` (clamped between 0% and 100%).
- **Reset Countdown (`formatResetTime`)**: Converts ISO 8601 `resetTime` into human-readable duration strings (e.g., `4h 10m`, `3d 20h`, `45m`, `30s`).
- **Progress Meter (`formatMiniBar`)**: Renders a 5-character high-visibility Unicode block meter:
  - `100%`: `▰▰▰▰▰`
  - `80%`: `▰▰▰▰▱`
  - `60%`: `▰▰▰▱▱`
  - `40%`: `▰▰▱▱▱`
  - `20%`: `▰▱▱▱▱`
  - `0%`: `▱▱▱▱▱`

---

## 6. Caching Architecture & Sub-Millisecond Statusline Performance

Statusline evaluations occur on every prompt and model interaction. Directly querying RPC endpoints on every evaluation would introduce 10–50ms latency. `agy-tools` implements a two-tier caching and refresh strategy:

```
[Statusline Evaluation]
         │
         ▼
[Read ~/.gemini/gemini_quota_cache.json] (<1ms synchronous read)
         │
         ├── Fresh (Age < 30s) ──► Recalculate countdown & render instantly
         │
         └── Stale (Age >= 30s) or Missing
                   │
                   ├── Render previous cached values immediately (no blocking)
                   └── Spawn detached non-blocking background refresh process (throttled to 10s)
```

### Cache File Schema (`~/.gemini/gemini_quota_cache.json`)
```json
{
  "version": 2,
  "timestamp": "2026-08-29T07:25:00.000Z",
  "timestampMs": 1787961900000,
  "isLive": true,
  "source": "language_server",
  "modelLabel": "Gemini Models",
  "remainPercent": 79,
  "remainingFraction": 0.792,
  "resetTime": "2026-08-29T11:35:12Z",
  "resetInSeconds": 15012,
  "resetFormatted": "4h 10m",
  "quota5h": {
    "remainPercent": 79,
    "remainingFraction": 0.792,
    "resetTime": "2026-08-29T11:35:12Z",
    "resetInSeconds": 15012,
    "resetFormatted": "4h 10m",
    "displayName": "5-Hour Limit",
    "window": "5h"
  },
  "quota7d": {
    "remainPercent": 21,
    "remainingFraction": 0.214,
    "resetTime": "2026-09-02T03:00:00Z",
    "resetInSeconds": 329700,
    "resetFormatted": "3d 20h",
    "displayName": "Weekly Limit",
    "window": "weekly"
  }
}
```

### Atomic File Writes
Cache writes use a `.tmp` file followed by `fs.renameSync` to ensure that concurrent statusline reads never read partially written or corrupt JSON files.

---

## 7. Graceful Degradation & Fallback

If the Language Server is stopped, offline, or inaccessible:
1. `agy-tools` switches to internal **Heuristic Rolling Usage Mode** (`aggregator.getRollingUsage`).
2. Calculates estimated 5h and 7d token usage from local conversation transcripts against configurable threshold limits (`DEFAULT_QUOTA_5H = 20M`, `DEFAULT_QUOTA_7D = 150M`).
3. Statusline displays estimated percentages without crashing or displaying error dialogs.

---

## 8. CLI Manual Synchronization

You can explicitly test and refresh the live Gemini quota pool at any time using the CLI:

```bash
# Fetch and print live quota status
agy-tokens --sync-quota

# Fetch and output as JSON
agy-tokens --sync-quota --json
```

Output example:
```
  ✔ Gemini quota pool synchronized (79% remaining, resets in 4h 10m).
  ↳ 5h: 79% (4h 10m) | 7d: 21% (3d 20h)
  ↳ C:\Users\<user>\.gemini\gemini_quota_cache.json
```
