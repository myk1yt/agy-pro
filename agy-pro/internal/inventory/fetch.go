package inventory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"agypro/internal/providers"
	"agypro/internal/realagy"
)

// DefaultFetchDeadline is the per-fetch subprocess bound for one `agy models`
// call (contract §2.5, F-Da). It is DISTINCT from the catalog cache TTL:
// conflating the two made a slow-but-successful fetch expire its own result.
const DefaultFetchDeadline = 15 * time.Second

// DefaultCacheTTL is the canonical catalog cache freshness bound (plan
// §4.2, ~5s). In-memory only - no disk cache.
const DefaultCacheTTL = 5 * time.Second

// NativeFetcher yields the native `agy models` catalog.
type NativeFetcher interface {
	Fetch(ctx context.Context) ([]NativeModel, error)
}

// RealAgyModels runs `<bin> models` and parses the QA-verified format (E4):
// a "Fetching available models..." banner line followed by `id<TAB>display`
// rows. Banner lines are skipped; lines without a TAB are ignored; empty
// parsed output is an error. Child env (F-ENV) is ALWAYS scrubbed via
// realagy.CleanChildEnv so a nested gateway shell can never turn the catalog
// into the 8-entry gateway list (E4 caveat).
type RealAgyModels struct {
	BinPath       string        // "" -> realagy.Resolve() at fetch (contract §2.5)
	FetchDeadline time.Duration // default 15s - per-fetch subprocess/network bound (F-Da)
	ScrubEnvNames []string      // provider APIKeyEnv names; set via NewRealAgyFetcher
	// Redact masks the failure text. NewRealAgyFetcher seeds it with the
	// registry's configured key values (the same seeded redactor the fanout CLI
	// uses); nil falls back to the unseeded generic shapes, which still masks
	// bearer/sk-/AIza/AWS/PEM material.
	Redact func(string) string
	Spawn  func(ctx context.Context, bin string, args ...string) *exec.Cmd // test seam
}

// NewRealAgyFetcher wires BinPath="" (resolve at fetch), FetchDeadline 15s and
// ScrubEnvNames = KeyScrubNames(reg). THE canonical constructor.
// Callers wrap it: Cached(NewRealAgyFetcher(reg), 5*time.Second).
func NewRealAgyFetcher(reg *providers.Registry) *RealAgyModels {
	return &RealAgyModels{
		FetchDeadline: DefaultFetchDeadline,
		ScrubEnvNames: KeyScrubNames(reg),
		Redact:        reg.Redactor(),
	}
}

// redactor returns the failure-text redactor, failing closed to the generic
// (unseeded) shapes when the caller supplied none.
func (r *RealAgyModels) redactor() func(string) string {
	if r.Redact != nil {
		return r.Redact
	}
	return providers.NewRedactor()
}

// KeyScrubNames is the provider-key env-name scrub union for the `agy models`
// fetch subprocess: every registry apiKeyEnv name UNION every compiled-in
// known-preset KeyEnv name (FIX-4 parity with hybrid.ChildKeyScrubNames, which
// the hybrid session children use).
//
// The union is load-bearing on its own, not just as parity: the preset list is
// independent of providers.json, so an exported preset key for a provider that
// is ABSENT from the registry (OPENAI_API_KEY, GROQ_API_KEY, ...) would
// otherwise ride into this subprocess - the S-3 class, on a spawn that exists
// precisely to read the user's environment. The registry-only list closed that
// hole only for providers the user had registered.
//
// It mirrors hybrid.ChildKeyScrubNames deliberately (same order, same skip
// rules); internal/inventory cannot import internal/hybrid without dragging the
// mesh/wireadp/wirecheck graph into every inventory consumer, and
// internal/realagy is outside this change's fence. The equality is pinned by
// TestKeyScrubNamesMatchesHybridUnion in fetch_test.go, so the two cannot drift.
func KeyScrubNames(reg *providers.Registry) []string {
	names := realagy.ApiKeyEnvNames(reg)
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		seen[n] = true
	}
	for _, h := range providers.KnownProviders() {
		n := strings.TrimSpace(h.KeyEnv)
		if n == "" {
			continue
		}
		key := strings.ToLower(n)
		if seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, n)
	}
	return names
}

// Fetch runs one `agy models` invocation and parses its catalog.
func (r *RealAgyModels) Fetch(ctx context.Context) ([]NativeModel, error) {
	if r == nil {
		return nil, errors.New("nil fetcher")
	}
	bin := strings.TrimSpace(r.BinPath)
	if bin == "" {
		resolved, err := realagy.Resolve()
		if err != nil {
			return nil, err
		}
		bin = resolved
	}

	deadline := r.FetchDeadline
	if deadline <= 0 {
		deadline = DefaultFetchDeadline
	}
	fetchCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	spawn := r.Spawn
	if spawn == nil {
		spawn = func(ctx context.Context, bin string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, bin, args...)
		}
	}

	cmd := spawn(fetchCtx, bin, "models")
	cmd.Env = realagy.CleanChildEnv(r.ScrubEnvNames)
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if fetchCtx.Err() != nil {
		return nil, fmt.Errorf("agy models fetch exceeded %s: %w", deadline, fetchCtx.Err())
	}
	if err != nil {
		// A9: the stderr tail is printed to the console and lands in
		// Inventory.NativeErr (which the launcher/settings surfaces render), so
		// it goes through the same seeded redactor the fanout CLI uses for
		// provider error snippets. `agy models` failures can echo credential
		// fragments.
		//
		// Redact BEFORE cutting, the ordering openaiup.Call and
		// wireadp.sanitizeMessage document: a secret the child positioned across
		// the cut must be whole when the redactor sees it, otherwise the cut
		// deletes the "bearer " prefix (or the sk-/AIza head) and leaves an
		// unshaped fragment that no pattern matches. Cutting first inverted that
		// rule and let a straddling fragment reach the terminal.
		redact := r.redactor()
		tail := redact(strings.TrimSpace(stderr.String()))
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		if tail != "" {
			return nil, fmt.Errorf("agy models failed: %v: %s", redact(err.Error()), tail)
		}
		return nil, fmt.Errorf("agy models failed: %v", redact(err.Error()))
	}

	models := parseModelsOutput(stdout.String())
	if len(models) == 0 {
		return nil, errors.New("agy models returned no parseable catalog rows")
	}
	return models, nil
}

// parseModelsOutput extracts `id<TAB>display` rows from real `agy models`
// output (Q6 fixture). Banner lines (no TAB) are skipped; a row's display
// name falls back to its id when the second column is empty.
func parseModelsOutput(out string) []NativeModel {
	var models []NativeModel
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		i := strings.IndexByte(line, '\t')
		if i < 0 {
			continue
		}
		id := strings.TrimSpace(line[:i])
		display := strings.TrimSpace(line[i+1:])
		if id == "" {
			continue
		}
		if display == "" {
			display = id
		}
		models = append(models, NativeModel{ID: id, DisplayName: display})
	}
	return models
}

// cachedFetcher wraps a NativeFetcher with a process-local TTL cache.
// TTL is the CACHE freshness bound; the per-fetch bound is FetchDeadline
// (F-Da) - the two are separate values by contract.
type cachedFetcher struct {
	inner NativeFetcher
	ttl   time.Duration

	mu     sync.Mutex
	stamp  time.Time
	models []NativeModel
	err    error
}

// Cached wraps any fetcher with a process-local TTL cache (canonical TTL 5s).
// In-memory only - no disk cache (honest freshness, zero secret surface).
func Cached(f NativeFetcher, ttl time.Duration) NativeFetcher {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &cachedFetcher{inner: f, ttl: ttl}
}

func (c *cachedFetcher) Fetch(ctx context.Context) ([]NativeModel, error) {
	c.mu.Lock()
	if !c.stamp.IsZero() && time.Since(c.stamp) < c.ttl {
		models, err := c.models, c.err
		c.mu.Unlock()
		return models, err
	}
	c.mu.Unlock()

	models, err := c.inner.Fetch(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	// A concurrent caller may have refreshed while we were fetching; keep the
	// newer stamp if so.
	if !c.stamp.IsZero() && time.Since(c.stamp) < c.ttl {
		return c.models, c.err
	}
	c.stamp = time.Now()
	c.models, c.err = models, err
	return models, err
}
