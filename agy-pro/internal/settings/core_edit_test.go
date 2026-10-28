package settings

// Provider-edit verb pins (P4-1). Every test runs against a temp registry
// (fileBackedCore pins EnvProviders, editCore additionally pins AGY_PRO_HOME
// into the same temp tree), so the live ~/.agy-pro/providers.json is never
// read or written. The file-backed harness uses the REAL Core seams
// (providers.Load / saveRegistryFile) so the on-disk bytes are the assertion
// target; the fakeDeps seam is used only where the call count of SaveReg
// itself is the subject.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// editFixture is the provider-edit fixture: two providers (so every pin also
// proves the sibling provider is untouched) and a target whose models carry
// every optional field, so a wholesale replace or a rebuilt struct literal
// cannot hide.
func editFixture() *providers.Registry {
	off := false
	return &providers.Registry{
		Defaults: providers.Defaults{N: 3, TimeoutSeconds: 900, Tools: "read,search", TokenBudget: 120000},
		Providers: []providers.Provider{
			{
				Name:      "acme",
				BaseURL:   "https://acme.example/v1",
				APIKeyEnv: "ACME_KEY",
				Models: []providers.Model{
					{
						ID: "acme-fast", Family: "acme", Tags: []string{"fast", "tools"},
						Enabled: &off, ContextLength: 128000, MaxOutputTokens: 4096,
						Capabilities: providers.Capabilities{
							SupportsTools: true, SupportsVision: true, SupportsThinking: true,
							ReasoningEffort: "high", AllowedTools: []string{"read", "search"},
						},
					},
					{ID: "acme-slow", ContextLength: 32000},
				},
			},
			{
				Name:      "beta",
				BaseURL:   "http://localhost:11434/v1",
				APIKeyEnv: "",
				Models:    []providers.Model{{ID: "beta-one"}},
			},
		},
	}
}

// editCore is fileBackedCore plus a temp AGY_PRO_HOME, so both registry
// location seams resolve inside the temp tree.
func editCore(t *testing.T, initial *providers.Registry) (*Core, string) {
	t.Helper()
	c, path := fileBackedCore(t, initial)
	t.Setenv(providers.EnvHome, filepath.Dir(path))
	return c, path
}

// exactProvider returns the provider whose name matches exactly. It must not
// go through Registry.Provider in the case-duplicate pins, where the whole
// point is that two records differ only by case.
func exactProvider(t *testing.T, reg *providers.Registry, name string) providers.Provider {
	t.Helper()
	for _, p := range reg.Providers {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("provider %q missing after reload (have %d providers)", name, len(reg.Providers))
	return providers.Provider{}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestProviderEdit_HappyPathChangesOnlyTwoFields is pin (a): the two endpoint
// fields change and the WHOLE record otherwise compares byte-identical, with
// the sibling provider and the registry defaults untouched.
func TestProviderEdit_HappyPathChangesOnlyTwoFields(t *testing.T) {
	c, path := editCore(t, editFixture())
	beforeReg := reloadFromDisk(t, path)
	beforeTarget := exactProvider(t, beforeReg, "acme")
	beforeSibling := exactProvider(t, beforeReg, "beta")

	if err := c.ProviderEdit("acme", "https://acme-new.example:8443/v2", "ACME_NEW_KEY"); err != nil {
		t.Fatalf("ProviderEdit: %v", err)
	}

	afterReg := reloadFromDisk(t, path)
	got := exactProvider(t, afterReg, "acme")
	if got.BaseURL != "https://acme-new.example:8443/v2" {
		t.Errorf("BaseURL on disk = %q", got.BaseURL)
	}
	if got.APIKeyEnv != "ACME_NEW_KEY" {
		t.Errorf("APIKeyEnv on disk = %q", got.APIKeyEnv)
	}
	if got.Name != beforeTarget.Name {
		t.Errorf("Name changed: %q -> %q", beforeTarget.Name, got.Name)
	}
	want := beforeTarget
	want.BaseURL = "https://acme-new.example:8443/v2"
	want.APIKeyEnv = "ACME_NEW_KEY"
	if jsonOf(t, got) != jsonOf(t, want) {
		t.Errorf("record changed beyond the two endpoint fields:\nbefore+edit: %s\nafter:       %s", jsonOf(t, want), jsonOf(t, got))
	}
	if jsonOf(t, exactProvider(t, afterReg, "beta")) != jsonOf(t, beforeSibling) {
		t.Error("sibling provider beta changed")
	}
	if jsonOf(t, afterReg.Defaults) != jsonOf(t, beforeReg.Defaults) {
		t.Error("registry defaults block changed")
	}
	for _, want := range []string{"acme-fast", "acme-slow", "beta-one", "https://acme-new.example:8443/v2"} {
		if !strings.Contains(string(diskBytes(t, path)), want) {
			t.Errorf("on-disk JSON lacks %q", want)
		}
	}
}

// TestProviderEdit_CaseFoldedTargetKeepsStoredName pins that identity
// resolution never rewrites the stored Name: editing "ACME" hits the record
// stored as "acme" and leaves its Name bytes alone.
func TestProviderEdit_CaseFoldedTargetKeepsStoredName(t *testing.T) {
	c, path := editCore(t, editFixture())
	if err := c.ProviderEdit("ACME", "https://acme-fold.example/v1", "ACME_KEY"); err != nil {
		t.Fatalf("ProviderEdit with folded name: %v", err)
	}
	got := exactProvider(t, reloadFromDisk(t, path), "acme")
	if got.Name != "acme" {
		t.Errorf("stored Name = %q, want the original spelling", got.Name)
	}
	if got.BaseURL != "https://acme-fold.example/v1" {
		t.Errorf("BaseURL = %q", got.BaseURL)
	}
}

// TestProviderEdit_IdempotentEditWritesNothing pins the idempotence contract:
// the same values are a no-op with NO write, at the seam level and on disk.
func TestProviderEdit_IdempotentEditWritesNothing(t *testing.T) {
	f := newFakeDeps()
	f.reg = editFixture()
	c := NewCore(f.deps())
	if err := c.ProviderEdit("acme", "https://acme.example/v1", "ACME_KEY"); err != nil {
		t.Fatalf("idempotent ProviderEdit: %v", err)
	}
	if f.saved != 0 {
		t.Errorf("SaveReg called %d times on an idempotent edit, want 0", f.saved)
	}

	fc, path := editCore(t, editFixture())
	before := diskBytes(t, path)
	// A case-folded name with unchanged values is the same no-op.
	if err := fc.ProviderEdit("ACME", "https://acme.example/v1", "ACME_KEY"); err != nil {
		t.Fatalf("idempotent ProviderEdit (folded name): %v", err)
	}
	if got := diskBytes(t, path); string(got) != string(before) {
		t.Error("on-disk bytes changed on an idempotent edit")
	}
}

// TestProviderEdit_UnknownTargetLeavesFileUntouched is pin (b).
func TestProviderEdit_UnknownTargetLeavesFileUntouched(t *testing.T) {
	c, path := editCore(t, editFixture())
	before := diskBytes(t, path)

	err := c.ProviderEdit("ghost", "https://ghost.example/v1", "GHOST_KEY")
	if !errors.Is(err, ErrProviderEditNotFound) {
		t.Fatalf("unknown name error = %v, want ErrProviderEditNotFound", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the (masked) operand: %v", err)
	}
	if got := diskBytes(t, path); string(got) != string(before) {
		t.Error("on-disk bytes changed after an unknown-name refusal")
	}

	if err := c.ProviderEdit("   ", "https://x.example/v1", "X_KEY"); !errors.Is(err, ErrProviderEditNotFound) {
		t.Errorf("empty name error = %v, want ErrProviderEditNotFound", err)
	}
	if got := diskBytes(t, path); string(got) != string(before) {
		t.Error("on-disk bytes changed after an empty-name refusal")
	}
}

// TestProviderEdit_BaseURLRefusalsLeaveFileUntouched is pin (c) for the base
// URL rule set: the twin's transport rules, nothing stricter or looser.
func TestProviderEdit_BaseURLRefusalsLeaveFileUntouched(t *testing.T) {
	cases := map[string]string{
		"empty":                   "",
		"whitespace only":         "   ",
		"no scheme":               "acme.example/v1",
		"unsupported scheme":      "ftp://acme.example/v1",
		"plain http, public host": "http://acme.example/v1",
		"space in host":           "https://acme example.com/v1",
	}
	for name, baseURL := range cases {
		t.Run(name, func(t *testing.T) {
			c, path := editCore(t, editFixture())
			before := diskBytes(t, path)
			err := c.ProviderEdit("acme", baseURL, "ACME_KEY")
			if !errors.Is(err, ErrProviderEditBaseURL) {
				t.Fatalf("error = %v, want ErrProviderEditBaseURL", err)
			}
			if got := diskBytes(t, path); string(got) != string(before) {
				t.Error("on-disk bytes changed after a baseURL refusal")
			}
			if got := exactProvider(t, reloadFromDisk(t, path), "acme"); got.BaseURL != "https://acme.example/v1" {
				t.Errorf("BaseURL on disk = %q, want the original", got.BaseURL)
			}
		})
	}
}

// TestProviderEdit_APIKeyEnvRefusalsLeaveFileUntouched is pin (c) for the
// env-NAME rule set, including the B4 credential-shape bypass.
func TestProviderEdit_APIKeyEnvRefusalsLeaveFileUntouched(t *testing.T) {
	secret := "sk-PLANTED-P4-1-0123456789abcdef"
	cases := map[string]string{
		"hyphenated name":     "ACME-KEY",
		"space in name":       "ACME KEY",
		"lowercase value":     "acme-key-value",
		"pasted key value":    secret,
		"pasted bearer-ish":   "bearer 0123456789abcdef",
		"pasted AWS key id":   testfixtures.AKIAExample,
		"path instead of var": "./keys/acme.env",
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			c, path := editCore(t, editFixture())
			before := diskBytes(t, path)
			err := c.ProviderEdit("acme", "https://acme.example/v1", env)
			if !errors.Is(err, ErrProviderEditAPIKeyEnv) {
				t.Fatalf("error = %v, want ErrProviderEditAPIKeyEnv", err)
			}
			if strings.Contains(err.Error(), env) {
				t.Errorf("error echoes the refused operand: %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error echoes key material: %v", err)
			}
			if got := diskBytes(t, path); string(got) != string(before) {
				t.Error("on-disk bytes changed after an apiKeyEnv refusal")
			}
		})
	}
}

// TestProviderEdit_RefusesUnprovableExistingEnvName pins the resulting-record
// rule: the guard applies to the whole record, so an edit cannot launder a
// legacy env name the add-json twin would refuse, and correcting that field in
// the same edit succeeds.
func TestProviderEdit_RefusesUnprovableExistingEnvName(t *testing.T) {
	seed := &providers.Registry{Providers: []providers.Provider{
		{
			Name: "acme", BaseURL: "https://acme.example/v1",
			// Fits the load-time charset (envNameRe), proves nothing else: not in
			// the environment, not a catalog name, not short UPPER_SNAKE.
			APIKeyEnv: "AcmeKeyVariable12",
			Models:    []providers.Model{{ID: "m1"}},
		},
	}}
	c, path := editCore(t, seed)
	if _, err := providers.LoadFrom(path); err != nil {
		t.Fatalf("seed must load: %v", err)
	}
	before := diskBytes(t, path)

	err := c.ProviderEdit("acme", "https://acme-moved.example/v1", "AcmeKeyVariable12")
	if !errors.Is(err, ErrProviderEditAPIKeyEnv) {
		t.Fatalf("error = %v, want ErrProviderEditAPIKeyEnv on the unchanged legacy env name", err)
	}
	if got := diskBytes(t, path); string(got) != string(before) {
		t.Error("on-disk bytes changed after the legacy-env-name refusal")
	}

	// The same edit with a corrected env name goes through.
	if err := c.ProviderEdit("acme", "https://acme-moved.example/v1", "ACME_KEY"); err != nil {
		t.Fatalf("corrected edit: %v", err)
	}
	got := exactProvider(t, reloadFromDisk(t, path), "acme")
	if got.BaseURL != "https://acme-moved.example/v1" || got.APIKeyEnv != "ACME_KEY" {
		t.Errorf("record after corrected edit = %+v", got)
	}
}

// TestProviderEdit_AcceptsTwinAllowedEndpoints keeps the mirror honest in the
// other direction: what the twin accepts must be accepted here, and no silent
// normalization may happen (the trailing slash and the explicit port survive).
func TestProviderEdit_AcceptsTwinAllowedEndpoints(t *testing.T) {
	urls := []string{
		"http://127.0.0.1:8080/v1",
		"http://localhost:11434/v1",
		"http://[::1]:9000/v1",
		"https://acme.example/v1/",
		"https://acme.example:8443/openai/v1",
	}
	for _, u := range urls {
		t.Run(u, func(t *testing.T) {
			c, path := editCore(t, editFixture())
			if err := c.ProviderEdit("acme", u, "ACME_KEY"); err != nil {
				t.Fatalf("ProviderEdit(%q): %v", u, err)
			}
			if got := exactProvider(t, reloadFromDisk(t, path), "acme").BaseURL; got != u {
				t.Errorf("BaseURL on disk = %q, want %q verbatim", got, u)
			}
		})
	}
}

// TestProviderEdit_ValidationParityWithTwin is the anti-drift pin: every
// payload is run through ProviderEdit AND through the twin's own mutation path
// (providers.UpsertProvider, which the add-json CLI verb and the wizard reach),
// and the two must return the same accept/refuse verdict. This is what keeps
// "mirrors the twin" true over time, including on inputs the twin does not
// reject (the twin's validateBaseURL requires no host for an https URL, so a
// bare "https://" is accepted by both: the verb inherits that gap rather than
// inventing a host rule the twin does not have).
func TestProviderEdit_ValidationParityWithTwin(t *testing.T) {
	single := func() *providers.Registry {
		return &providers.Registry{Providers: []providers.Provider{{
			Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{{ID: "m1"}},
		}}}
	}
	cases := []struct {
		name    string
		baseURL string
		env     string
	}{
		{name: "https", baseURL: "https://acme.example/v1", env: "ACME_KEY"},
		{name: "loopback http", baseURL: "http://127.0.0.1:8080/v1", env: "ACME_KEY"},
		{name: "empty baseURL", baseURL: "", env: "ACME_KEY"},
		{name: "no scheme", baseURL: "acme.example/v1", env: "ACME_KEY"},
		{name: "unsupported scheme", baseURL: "ftp://acme.example/v1", env: "ACME_KEY"},
		{name: "public http", baseURL: "http://acme.example/v1", env: "ACME_KEY"},
		{name: "https without host", baseURL: "https://", env: "ACME_KEY"},
		{name: "env with hyphen", baseURL: "https://acme.example/v1", env: "ACME-KEY"},
		{name: "env key value", baseURL: "https://acme.example/v1", env: "sk-parity-0123456789abcdef"},
		{name: "env empty", baseURL: "https://acme.example/v1", env: ""},
		{name: "env unprovable name", baseURL: "https://acme.example/v1", env: "AcmeKeyVariable12"},
		{name: "env aws key id", baseURL: "https://acme.example/v1", env: testfixtures.AKIAExample},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDeps()
			f.reg = single()
			editErr := NewCore(f.deps()).ProviderEdit("acme", tc.baseURL, tc.env)

			twin := single()
			twinErr := providers.UpsertProvider(twin, providers.Provider{
				Name: "acme", BaseURL: tc.baseURL, APIKeyEnv: tc.env, Models: []providers.Model{{ID: "m1"}},
			})

			if (editErr == nil) != (twinErr == nil) {
				t.Fatalf("verdict drift: ProviderEdit(%q, %q) = %v, twin = %v", tc.baseURL, tc.env, editErr, twinErr)
			}
			if editErr != nil && f.saved != 0 {
				t.Errorf("SaveReg called %d times on a refusal", f.saved)
			}
		})
	}
}

// TestProviderEdit_ClearsAPIKeyEnvOnEmpty pins the optional-field parity: an
// empty apiKeyEnv clears the slot (a provider with no key) and is accepted.
func TestProviderEdit_ClearsAPIKeyEnvOnEmpty(t *testing.T) {
	c, path := editCore(t, editFixture())
	if err := c.ProviderEdit("acme", "https://acme.example/v1", ""); err != nil {
		t.Fatalf("ProviderEdit clearing apiKeyEnv: %v", err)
	}
	got := exactProvider(t, reloadFromDisk(t, path), "acme")
	if got.APIKeyEnv != "" {
		t.Errorf("APIKeyEnv = %q, want cleared", got.APIKeyEnv)
	}
	if strings.Contains(jsonOf(t, got), "apiKeyEnv") {
		t.Error("cleared apiKeyEnv still serialises a value")
	}
	if !strings.Contains(jsonOf(t, got), "acme-fast") {
		t.Error("clearing apiKeyEnv dropped the models")
	}
}

// TestProviderEdit_CaseDuplicateRegistryIdentity is pin (d): the documented
// identity rule against a registry that legally holds "acme" and "ACME"
// (Registry.Validate's duplicate check is exact-case).
func TestProviderEdit_CaseDuplicateRegistryIdentity(t *testing.T) {
	dupFixture := func() *providers.Registry {
		return &providers.Registry{Providers: []providers.Provider{
			{Name: "acme", BaseURL: "https://first.example/v1", APIKeyEnv: "FIRST_KEY", Models: []providers.Model{{ID: "m-first"}}},
			{Name: "ACME", BaseURL: "https://second.example/v1", APIKeyEnv: "SECOND_KEY", Models: []providers.Model{{ID: "m-second"}}},
		}}
	}

	t.Run("exact match hits only its own record", func(t *testing.T) {
		c, path := editCore(t, dupFixture())
		if err := c.ProviderEdit("ACME", "https://second-moved.example/v1", "SECOND_KEY"); err != nil {
			t.Fatalf("exact-case edit: %v", err)
		}
		reg := reloadFromDisk(t, path)
		first := exactProvider(t, reg, "acme")
		second := exactProvider(t, reg, "ACME")
		if first.BaseURL != "https://first.example/v1" {
			t.Errorf("the other case-duplicate was edited: %+v", first)
		}
		if second.BaseURL != "https://second-moved.example/v1" {
			t.Errorf("exact target was not edited: %+v", second)
		}
	})

	t.Run("case-folded name without an exact match is ambiguous", func(t *testing.T) {
		c, path := editCore(t, dupFixture())
		before := diskBytes(t, path)
		err := c.ProviderEdit("Acme", "https://anywhere.example/v1", "FIRST_KEY")
		if !errors.Is(err, ErrProviderEditAmbiguous) {
			t.Fatalf("error = %v, want ErrProviderEditAmbiguous", err)
		}
		if got := diskBytes(t, path); string(got) != string(before) {
			t.Error("on-disk bytes changed after an ambiguous-target refusal")
		}

		// The seam-level pin: no save is attempted either.
		f := newFakeDeps()
		f.reg = dupFixture()
		if err := NewCore(f.deps()).ProviderEdit("Acme", "https://anywhere.example/v1", "FIRST_KEY"); !errors.Is(err, ErrProviderEditAmbiguous) {
			t.Errorf("fake-seam error = %v, want ErrProviderEditAmbiguous", err)
		}
		if f.saved != 0 {
			t.Errorf("SaveReg called %d times on an ambiguous target", f.saved)
		}
	})
}

// TestProviderEdit_SiblingModelsSurvive is pin (e): the on-disk result keeps
// the sibling models a wholesale UpsertProvider replace would have dropped.
func TestProviderEdit_SiblingModelsSurvive(t *testing.T) {
	c, path := editCore(t, editFixture())
	beforeModels := jsonOf(t, exactProvider(t, reloadFromDisk(t, path), "acme").Models)

	if err := c.ProviderEdit("acme", "https://acme-new.example/v2", "ACME_NEW_KEY"); err != nil {
		t.Fatalf("ProviderEdit: %v", err)
	}
	gotModels := exactProvider(t, reloadFromDisk(t, path), "acme").Models
	if len(gotModels) != 2 {
		t.Fatalf("models after edit = %d, want 2: %+v", len(gotModels), gotModels)
	}
	if jsonOf(t, gotModels) != beforeModels {
		t.Errorf("models changed:\nbefore: %s\nafter:  %s", beforeModels, jsonOf(t, gotModels))
	}

	// Negative control: the trap this verb exists to avoid. UpsertProvider with
	// the same two-field payload REPLACES the record and drops the models, so the
	// assertion above is a real discriminator and not a tautology.
	scratch := editFixture()
	if err := providers.UpsertProvider(scratch, providers.Provider{
		Name: "acme", BaseURL: "https://acme-new.example/v2", APIKeyEnv: "ACME_NEW_KEY",
	}); err != nil {
		t.Fatalf("control UpsertProvider: %v", err)
	}
	if dropped := len(scratch.Providers[0].Models); dropped != 0 {
		t.Fatalf("control replace kept %d models; the trap fixture no longer models the wholesale replace", dropped)
	}
}

// TestProviderEdit_NeverCallsUpsertProvider is the structural half of pin (e):
// the verb's own body must not reach for UpsertProvider or build a provider
// record literal (ProviderAdd in the same file legitimately does both, so the
// check is scoped to the ProviderEdit body).
func TestProviderEdit_NeverCallsUpsertProvider(t *testing.T) {
	src, err := os.ReadFile("core.go")
	if err != nil {
		t.Fatalf("read core.go: %v", err)
	}
	text := string(src)
	const sig = "func (c *Core) ProviderEdit("
	i := strings.Index(text, sig)
	if i < 0 {
		t.Fatal("ProviderEdit is not defined in core.go")
	}
	rest := text[i+len(sig):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		rest = rest[:j]
	}
	if strings.Contains(rest, "UpsertProvider") {
		t.Error("ProviderEdit body references UpsertProvider; it must mutate the two fields in place")
	}
	if strings.Contains(rest, "Provider{") {
		t.Error("ProviderEdit body builds a provider record literal; it must mutate the loaded record in place")
	}
}

// TestProviderEdit_ReloadsRegistryEachCall pins that a stale copy is never
// trusted: a change that landed on disk after the Core was built is seen (and
// preserved) by the next edit.
func TestProviderEdit_ReloadsRegistryEachCall(t *testing.T) {
	c, path := editCore(t, editFixture())

	external := editFixture()
	external.Providers = append(external.Providers, providers.Provider{
		Name: "gamma", BaseURL: "https://gamma.example/v1", APIKeyEnv: "GAMMA_KEY",
		Models: []providers.Model{{ID: "gamma-one"}},
	})
	external.Path = path
	if err := saveRegistryFile(external); err != nil {
		t.Fatalf("external write: %v", err)
	}

	if err := c.ProviderEdit("acme", "https://acme-new.example/v2", "ACME_NEW_KEY"); err != nil {
		t.Fatalf("ProviderEdit: %v", err)
	}
	reg := reloadFromDisk(t, path)
	if _, ok := reg.Provider("gamma"); !ok {
		t.Error("provider added on disk after Core construction was lost; the verb trusted a stale registry")
	}
	if got := exactProvider(t, reg, "acme").BaseURL; got != "https://acme-new.example/v2" {
		t.Errorf("BaseURL = %q", got)
	}
}

// TestProviderEdit_APIKeyEnvNeverCarriesAValue is pin (f): the slot takes a
// NAME, a pasted value is refused with a value-free error, and no value ever
// reaches the file even when the name resolves in the environment.
func TestProviderEdit_APIKeyEnvNeverCarriesAValue(t *testing.T) {
	secret := "sk-PLANTED-P4-1-NAMEONLY-0123456789"
	c, path := editCore(t, editFixture())
	t.Setenv("ACME_NEW_KEY", secret)

	if err := c.ProviderEdit("acme", "https://acme.example/v1", "ACME_NEW_KEY"); err != nil {
		t.Fatalf("ProviderEdit with a resolving name: %v", err)
	}
	onDisk := string(diskBytes(t, path))
	if !strings.Contains(onDisk, "ACME_NEW_KEY") {
		t.Error("the env-var NAME was not persisted")
	}
	if strings.Contains(onDisk, secret) {
		t.Error("the env VALUE reached the registry file")
	}
	if got := exactProvider(t, reloadFromDisk(t, path), "acme").APIKeyEnv; got != "ACME_NEW_KEY" {
		t.Errorf("APIKeyEnv = %q, want the name", got)
	}

	err := c.ProviderEdit("acme", "https://acme.example/v1", secret)
	if !errors.Is(err, ErrProviderEditAPIKeyEnv) {
		t.Fatalf("error = %v, want ErrProviderEditAPIKeyEnv", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "PLANTED") {
		t.Errorf("error echoes key material: %v", err)
	}
	if strings.Contains(string(diskBytes(t, path)), secret) {
		t.Error("the refused value reached the registry file")
	}
}

// TestProviderEdit_SeamSavesOnlyOnSuccess pins the write discipline at the seam
// level: rejected and idempotent edits never call SaveReg; exactly one save per
// successful mutation.
func TestProviderEdit_SeamSavesOnlyOnSuccess(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		env      string
		wantErr  error
		wantSave int
	}{
		{name: "valid", baseURL: "https://acme-new.example/v2", env: "ACME_NEW_KEY", wantSave: 1},
		{name: "idempotent", baseURL: "https://acme.example/v1", env: "ACME_KEY", wantSave: 0},
		{name: "bad baseURL", baseURL: "acme.example", env: "ACME_KEY", wantErr: ErrProviderEditBaseURL},
		{name: "bad env", baseURL: "https://acme.example/v1", env: "sk-nope-0123456789abcdef", wantErr: ErrProviderEditAPIKeyEnv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDeps()
			f.reg = editFixture()
			c := NewCore(f.deps())
			err := c.ProviderEdit("acme", tc.baseURL, tc.env)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if f.saved != 0 {
					t.Errorf("SaveReg called %d times on a refusal", f.saved)
				}
				// The in-memory record must be rolled back, not half-edited.
				if got := exactProvider(t, f.reg, "acme"); got.BaseURL != "https://acme.example/v1" || got.APIKeyEnv != "ACME_KEY" {
					t.Errorf("record left half-edited: %+v", got)
				}
				return
			}
			if f.saved != tc.wantSave {
				t.Errorf("SaveReg called %d times, want %d", f.saved, tc.wantSave)
			}
		})
	}
}

// Pins for the exported edit-target resolver (fix round, unit fix-1 FIX-B). The
// TUI provider form's stale-base gate resolves its target through this function,
// so the identity rule has to be one rule: these pins fix its verdicts on a legal
// case-duplicate registry and prove the verb applies the same index.

// editTargetFixture is a registry Registry.Validate permits: the duplicate check
// is exact-case, so "acme" and "ACME" coexist and a case-insensitive resolver sees
// two matches for one name. "beta" is an unrelated sibling.
func editTargetFixture() *providers.Registry {
	return &providers.Registry{Providers: []providers.Provider{
		{Name: "acme", BaseURL: "https://first.example/v1", APIKeyEnv: "FIRST_KEY", Models: []providers.Model{{ID: "m-first"}}},
		{Name: "ACME", BaseURL: "https://second.example/v1", APIKeyEnv: "SECOND_KEY", Models: []providers.Model{{ID: "m-second"}}},
		{Name: "beta", BaseURL: "https://beta.example/v1", Models: []providers.Model{{ID: "m-beta"}}},
	}}
}

// TestResolveProviderEditTarget_SharedIdentityRule pins the resolver's verdicts:
// an exact-case match wins outright, a case-folded name resolves only when it is
// the single match, and everything else is a classified refusal.
func TestResolveProviderEditTarget_SharedIdentityRule(t *testing.T) {
	reg := editTargetFixture()
	cases := []struct {
		name    string
		in      string
		wantIdx int
		wantErr error
	}{
		{"exact lower", "acme", 0, nil},
		{"exact upper", "ACME", 1, nil},
		{"exact wins over the fold", "beta", 2, nil},
		{"padded operand trims", "  ACME  ", 1, nil},
		{"single fold match", "BeTa", 2, nil},
		{"ambiguous fold", "Acme", -1, ErrProviderEditAmbiguous},
		{"unknown", "ghost", -1, ErrProviderEditNotFound},
		{"empty", "   ", -1, ErrProviderEditNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx, err := ResolveProviderEditTarget(reg, tc.in)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ResolveProviderEditTarget(%q) error = %v, want nil", tc.in, err)
				}
				if idx != tc.wantIdx {
					t.Fatalf("ResolveProviderEditTarget(%q) = %d, want %d", tc.in, idx, tc.wantIdx)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResolveProviderEditTarget(%q) error = %v, want %v", tc.in, err, tc.wantErr)
			}
			if idx != -1 {
				t.Fatalf("ResolveProviderEditTarget(%q) = %d on a refusal, want -1", tc.in, idx)
			}
		})
	}
}

// TestResolveProviderEditTarget_AgreesWithVerb pins the property the TUI guard
// depends on: the verb's verdict and the resolver's verdict are the same verdict,
// and a successful edit moves exactly the record the resolver named. A front-end
// gate built on this function therefore cannot refuse a legal edit (the FIX-B
// false refusal) or pass while the verb writes a different record (the FIX-B
// silent revert).
func TestResolveProviderEditTarget_AgreesWithVerb(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"exact upper", "ACME", nil},
		{"exact lower", "acme", nil},
		{"single fold", "BeTa", nil},
		{"ambiguous fold", "Acme", ErrProviderEditAmbiguous},
		{"unknown", "ghost", ErrProviderEditNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, path := editCore(t, editTargetFixture())
			seeded := reloadFromDisk(t, path)
			idx, resolveErr := ResolveProviderEditTarget(seeded, tc.in)
			before := diskBytes(t, path)

			// The new endpoint value names its target, so the pin can tell WHICH
			// record the verb touched.
			const movedBase = "https://moved.example/v9"
			verbErr := c.ProviderEdit(tc.in, movedBase, "")

			if tc.wantErr != nil {
				if !errors.Is(resolveErr, tc.wantErr) {
					t.Fatalf("resolver error = %v, want %v", resolveErr, tc.wantErr)
				}
				if !errors.Is(verbErr, tc.wantErr) {
					t.Fatalf("verb error = %v, want the resolver's class %v", verbErr, tc.wantErr)
				}
				if got := diskBytes(t, path); string(got) != string(before) {
					t.Fatal("providers.json changed although both the resolver and the verb refused")
				}
				return
			}
			if resolveErr != nil {
				t.Fatalf("resolver refused %q: %v", tc.in, resolveErr)
			}
			if verbErr != nil {
				t.Fatalf("resolver accepted %q (index %d) but the verb refused: %v", tc.in, idx, verbErr)
			}
			reg := reloadFromDisk(t, path)
			if got := reg.Providers[idx]; got.BaseURL != movedBase {
				t.Fatalf("the verb did not edit the record the resolver named (index %d): %+v", idx, got)
			}
			for i := range reg.Providers {
				if i != idx && reg.Providers[i].BaseURL == movedBase {
					t.Fatalf("the verb edited record %d as well: %+v", i, reg.Providers[i])
				}
			}
		})
	}
}
