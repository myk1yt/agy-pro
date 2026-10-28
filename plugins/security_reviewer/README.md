# Security Reviewer - Enterprise Multi-Agent Security Audit Suite

[![Antigravity Plugin](https://img.shields.io/badge/Antigravity-Plugin-blue.svg)](https://github.com/google/antigravity)
[![Security Auditing](https://img.shields.io/badge/Audit-OWASP%20%7C%20CWE%20%7C%20IAM-red.svg)](#overview-of-specialized-domain-inspectors)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey.svg)](#quick-start-one-click-installation)

Lead Security Reviewer orchestrating specialized domain subagents for pre-commit audits: OWASP/CWE, Secrets/Credentials, Cloud/IaC, and Antigravity MCP privileges. Built on Everything Gemini Code (EGC) pre-commit quality gate patterns, OWASP Top 10, CWE Top 25, and Google Cloud `roles/iam.securityReviewer` least-privilege principles, Security Reviewer orchestrates 4 specialized domain inspectors to conduct deep, read-only security audits across your codebase.

---

## 🚀 Quick Start (One-Click Installation)

Open your terminal in the repository root directory and run the single command for your platform:

### Windows (PowerShell)
```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-security-reviewer.ps1
```

> 💡 **Beginner Tip (초보자 / 컴맹을 위한 팁)**:
> In Windows File Explorer, press **Shift + Right-Click** in an empty area inside the repository folder and select **"Open PowerShell window here"** or **"Open in Terminal"**, then copy and paste the command above and press Enter.

### macOS / Linux (Terminal)
```bash
bash scripts/install-security-reviewer.sh
```

> **What the installer does automatically**:
> 1. Registers the `security_reviewer` plugin package with Antigravity CLI via `agy plugin install`.
> 2. Verifies the active agent list via `agy agents`.

---

## 🔍 Overview of Specialized Domain Inspectors

When you invoke the lead orchestrator (`security-reviewer`), it concurrently coordinates 4 domain-specific inspection subagents:

| Inspector | Primary Focus & Coverage |
|---|---|
| **`sec-app-vuln`** | **OWASP Top 10 (2021) & CWE Top 25**<br>Analyzes SQL Injection, Cross-Site Scripting (XSS), Path Traversal, SSRF, Indirect Prompt Injection, and ReDoS/Rate Limiting vulnerabilities. |
| **`sec-cloud-iam`** | **GCP IAM & Infrastructure as Code (IaC)**<br>Audits GCP `roles/iam.securityReviewer` least privilege policies, Terraform, Kubernetes manifests, Dockerfile security, service account keys, and overly permissive firewall rules (`0.0.0.0/0`, `allUsers`). |
| **`sec-credential-scanner`** | **Secrets, Keys & Data Leakage**<br>Detects hardcoded API keys, private certificates, JWT tokens, uncommitted `.env` files, and PII leakage in logs (with intelligent test/mock fixture exemptions). |
| **`sec-supply-mcp`** | **Supply Chain, MCP & Dependency Governance**<br>Inspects package vulnerabilities (CVEs), MCP tool definitions, runtime capability privileges, and web security headers (CORS/CSP). |

---

## 💡 How to Use in Antigravity

You can invoke the Security Reviewer inside Antigravity CLI in two convenient ways:

### Method 1: Using the `/agent` Menu

Type `/agent` in the Antigravity prompt and choose `security-reviewer`:

```text
┌────────────────────────────────────────────────────────┐
│ Select an Agent                                        │
├────────────────────────────────────────────────────────┤
│   designer          (Zero-MCP Design Specialist)       │
│ > security-reviewer (Enterprise Multi-Agent Audit)     │
│   agy_help          (Antigravity Ecosystem Guide)      │
└────────────────────────────────────────────────────────┘
  ▲/▼: Navigate   Enter: Select   Esc: Cancel
```

### Method 2: Direct Mention (`@security-reviewer`)

Mention `@security-reviewer` directly in your chat:

#### Full Pre-Commit Security Audit
```text
@security-reviewer Perform a pre-commit security audit on all changed files in this repository before pushing to production.
```

#### Application Vulnerability & Injection Check
```text
@security-reviewer Audit src/api/routes/auth.js and src/db/queries.js for SQL injection, SSRF, and authentication bypass vulnerabilities.
```

#### Cloud IAM & Infrastructure Security Audit
```text
@security-reviewer Review terraform/iam.tf and k8s/deployment.yaml for over-privileged IAM roles, public bucket exposures, and Docker root-user configurations.
```

#### Secrets & Credential Scan
```text
@security-reviewer Scan the entire codebase for accidentally committed API tokens, service account credentials, and private keys.
```

---

## 📋 Standardized Audit Deliverable

Audits produce an actionable 4-part EGC report:
1. **[1. Philosophy Alignment]**: Environment context (Production / Staging), compliance frameworks (SOC2, GDPR, PCI-DSS).
2. **[2. Audit Scope]**: Explicit inventory of reviewed files and dispatched subagents.
3. **[3. Consolidated Findings]**: Ranked by severity (🔴 Critical, 🟠 High, 🟡 Medium, 🟢 Low) with exact code locations, exploit PoCs, and copy-paste remediation diffs.
4. **[4. Post-Audit Quality Gate Verdict]**: `PASS`, `CONDITIONAL PASS`, or `FAIL` with clear deployment blockers.

---

## 🗑️ One-Click Uninstallation

To cleanly remove the Security Reviewer plugin:

### Windows (PowerShell)
```powershell
powershell -ExecutionPolicy Bypass -File scripts/uninstall-security-reviewer.ps1
```

### macOS / Linux (Terminal)
```bash
bash scripts/uninstall-security-reviewer.sh
```

---

## 🛡️ License

MIT © Google Antigravity Team & Contributors
