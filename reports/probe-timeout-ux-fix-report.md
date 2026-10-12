# Wizard Probe Timeout & Streamline Retry UX Fix Report

## Executive Summary

This report documents the resolution of timeout issues and UX friction within the interactive provider setup wizard (`InteractiveSetupWizard` in [`agy-pro/internal/providers/wizard.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/wizard.go)). External inference gateways such as NeuralWatt and multi-cloud routing providers often experience cold starts and traffic queueing during initial endpoint probing, exceeding the previous 10-second deadline. Additionally, probe failures previously forced redundant credential re-entry, causing user confusion.

All timeout limits have been raised to **35 seconds**, the failure recovery loop has been redesigned with 4 intuitive options (defaulting to safe save), credentials preservation during multi-model addition has been guaranteed, and full unit test coverage and binary deployment have been completed.

---

## Key Modifications

### 1. Probe Timeout Increased to 35 Seconds
- **Before**: `10*time.Second` in [`ProbeEndpoint`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/wizard.go#L470-L485) (`context.WithTimeout` and `http.Client.Timeout`). NeuralWatt gateway cold-start probes taking 8.8s+ were at risk of false timeouts.
- **After**: **35 seconds** (`35*time.Second`) configured for both the request context timeout and the HTTP client timeout. This accommodates cold-start latency, queue wait times, and gateway handshakes.

### 2. Streamlined Probe Failure UX & Retry Options
When an endpoint probe fails, the wizard now provides four clear actions:
1. **`1) Save model configuration anyway [Recommended]`**: Default option on pressing `Enter`. Permits users to store their configuration even if the gateway is temporarily unreachable or requires specialized request parameters.
2. **`2) Retry test immediately`**: Directly reruns the live probe using the existing `baseURL` and `apiKey` without prompting for any re-entry.
3. **`3) Edit Base URL or API Key`**: Displays current values as defaults (`Base URL [<current_url>]:`, `API Key [keep current]:`). Pressing `Enter` preserves existing credentials; entering a new value updates the session and Windows User Environment.
4. **`4) Discard model`**: Discards the unverified model and continues the setup session.

### 3. Gateway Timeout Guidance Notice
When a timeout is detected (via `context.DeadlineExceeded`, `net.Error.Timeout()`, or matching timeout error messages), a banner is displayed:
```text
[Notice] Gateway response timed out. The model may be cold-starting or queueing. You can retry immediately or save anyway.
```

### 4. Credential Preservation in Multi-Model Loop (`Add another model?`)
- When configuring multiple models under the same provider (e.g., `Add another model for NW? (y/N)`), the established `baseURL` and `keyValue` are preserved in the outer scope.
- Subsequent iterations strictly skip Base URL and API Key collection, directly presenting model selection and performing the live probe.

---

## Unit Testing & Verification

The test suite in [`agy-pro/internal/providers/providers_test.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/providers_test.go) was expanded to validate all scenarios:

| Test Name | Purpose | Result |
| :--- | :--- | :--- |
| `TestIsTimeoutErr` | Validates error identification for deadline exceeded and timeout strings | **PASS** |
| `TestProbeEndpoint` | Validates endpoint probing, latency measurement, and auth rejection | **PASS** |
| `TestInteractiveSetupWizard_ProbeFailure_SaveAnyway` | Verifies default option 1 saves model configuration on failure | **PASS** |
| `TestInteractiveSetupWizard_ProbeFailure_RetryImmediately` | Verifies option 2 immediately re-probes without credential re-entry | **PASS** |
| `TestInteractiveSetupWizard_ProbeFailure_EditAndRetry` | Verifies option 3 permits editing/retaining keys and succeeds | **PASS** |
| `TestInteractiveSetupWizard_ProbeFailure_TimeoutNotice` | Verifies gateway timeout notice banner appears upon timeout | **PASS** |
| `TestInteractiveSetupWizard_MultipleModels_PreservesCredentials` | Verifies 2+ models configured in loop share credentials without re-prompting | **PASS** |

### Test Suite Execution
```text
=== RUN   TestInteractiveSetupWizard_ProbeFailure_SaveAnyway
--- PASS: TestInteractiveSetupWizard_ProbeFailure_SaveAnyway (0.73s)
=== RUN   TestInteractiveSetupWizard_ProbeFailure_RetryImmediately
--- PASS: TestInteractiveSetupWizard_ProbeFailure_RetryImmediately (0.71s)
=== RUN   TestInteractiveSetupWizard_ProbeFailure_EditAndRetry
--- PASS: TestInteractiveSetupWizard_ProbeFailure_EditAndRetry (1.33s)
=== RUN   TestInteractiveSetupWizard_ProbeFailure_TimeoutNotice
--- PASS: TestInteractiveSetupWizard_ProbeFailure_TimeoutNotice (0.60s)
=== RUN   TestInteractiveSetupWizard_MultipleModels_PreservesCredentials
--- PASS: TestInteractiveSetupWizard_MultipleModels_PreservesCredentials (0.67s)
PASS
ok      agypro/internal/providers       8.348s
```
- `go vet ./...`: 0 warnings, clean.
- `go test ./...`: All packages passed.

---

## Binary Build & Deployment

1. Built fresh `agy.exe` binary from `agy-pro/cmd/agy`.
2. Existing executable backed up to `C:\Users\k1yt\.agy-pro\bin\backup\agy_old.exe`.
3. Newly compiled executable atomically deployed to `C:\Users\k1yt\.agy-pro\bin\agy.exe`.
4. Verified CLI functionality via `agy.exe pro help`.
