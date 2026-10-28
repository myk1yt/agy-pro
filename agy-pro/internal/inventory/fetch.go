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
	BinPath       string                                                          // "" -> realagy.Resolve() at fetch (contract §2.5)
	FetchDeadline time.Duration                                                   // default 15s - per-fetch subprocess/network bound (F-Da)
	ScrubEnvNames []string                                                        // provider APIKeyEnv names; set via NewRealAgyFetcher
	Spawn         func(ctx context.Context, bin string, args ...string) *exec.Cmd // test seam
}

// NewRealAgyFetcher wires BinPath="" (resolve at fetch), FetchDeadline 15s and
// ScrubEnvNames = realagy.ApiKeyEnvNames(reg). THE canonical constructor.
// Callers wrap it: Cached(NewRealAgyFetcher(reg), 5*time.Second).
func NewRealAgyFetcher(reg *providers.Registry) *RealAgyModels {
	return &RealAgyModels{
		FetchDeadline: DefaultFetchDeadline,
		ScrubEnvNames: realagy.ApiKeyEnvNames(reg),
	}
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
		// it goes through the same Redactor the fanout CLI uses for provider
		// error snippets. `agy models` failures can echo credential fragments.
		redact := providers.NewRedactor()
		tail := strings.TrimSpace(stderr.String())
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		if tail != "" {
			return nil, fmt.Errorf("agy models failed: %v: %s", redact(err.Error()), redact(tail))
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
