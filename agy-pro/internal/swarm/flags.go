package swarm

// Non-interactive `agy pro swarm` flag surface (contract U3/C2). The
// interactive builder can edit every field of the run spec: the template
// (task), the tools allow-list, the timeout and the free-text prompt
// (builder.go editField). Before A2 the flag surface had no equivalent for
// two of them: `--tools` and `--task` existed only as fields the builder
// edited interactively, so a CI/power-user run could not reproduce an
// interactive build. This file is the swarm-owned parsing half; the cmd/agy
// dispatch surface wires it in.

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"agypro/internal/fanout"
)

// SwarmFlagNames lists the non-interactive flag names in usage order. The F-B
// rule keys on PRESENCE of any of them, not on their values.
var SwarmFlagNames = []string{
	"template", "seats", "root", "prompt", "prompt-file", "tools", "task", "timeout", "dry-run",
}

// SwarmUsage is the `agy pro swarm` usage text (A2: it documents --tools and
// --task, the two flag equivalents of the interactive edits). The timeout
// default is rendered from fanout.DefaultTimeoutSec (the single source of
// truth shared with the fanout CLI flag), so the text cannot drift from the
// constant the way the old hardcoded "120" did.
var SwarmUsage = fmt.Sprintf(`Usage: agy pro swarm [options]

Interactive (default): numbered template + seat builder (needs a TTY).
Non-interactive (any option below present; zero prompts):
  --template NAME     code-review | security-audit | custom   (required)
  --seats SPEC        numbered selection ("1,3,5", "2-4", "p1".."p3")
                      or explicit seat specs ("native:id,prov/model")  (required)
  --root DIR          workspace root (default ".")
  --prompt TEXT       user prompt (default: template scaffold)
  --prompt-file PATH  prompt file, or '-' for stdin
  --tools SPEC        allowed tools: read,search,write,exec (closed set;
                      unknown tokens are rejected). Default: the template's tools
  --task NAME         fanout task type: review|security|plan-audit|research|
                      ideas|custom (free text). Default: the template's task
  --timeout SECONDS   swarm timeout (default %d)
  --dry-run           print the equivalent "agy pro fanout" command and exit 0

Seat assignment is numbered selection in the happy path; flags are the
power-user/CI fallback.`, fanout.DefaultTimeoutSec)

// SwarmFlags is the parsed non-interactive flag surface.
type SwarmFlags struct {
	Template   string
	Seats      string
	Root       string
	Prompt     string
	PromptFile string
	Tools      string
	Task       string
	Timeout    int
	DryRun     bool

	// Set records which flags were actually present on the command line
	// (F-B: presence, not value, decides interactive vs non-interactive).
	Set map[string]bool
}

// NonInteractive reports whether any non-interactive flag was present (F-B).
func (f SwarmFlags) NonInteractive() bool { return len(f.Set) > 0 }

// ParseSwarmFlags parses the `agy pro swarm` non-interactive flag surface.
// It returns (flags, 0) on success or explicit help and (zero, 2) on any other
// parse error or a positional argument, with the usage text written to errw.
func ParseSwarmFlags(args []string, errw io.Writer) (SwarmFlags, int) {
	fs := flag.NewFlagSet("swarm", flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.Usage = func() { fmt.Fprintln(errw, SwarmUsage) }

	var (
		templateFlag = fs.String("template", "", "swarm template: code-review|security-audit|custom")
		seatsFlag    = fs.String("seats", "", "seat selection: numbered ('1,3,5', '2-4', 'p1'..'p3') or explicit specs")
		rootFlag     = fs.String("root", ".", "workspace root directory")
		promptFlag   = fs.String("prompt", "", "inline user prompt text")
		promptFile   = fs.String("prompt-file", "", "path to prompt file, or '-' for stdin")
		toolsFlag    = fs.String("tools", "", "allowed tools: read,search,write,exec (default: the template's tools)")
		taskFlag     = fs.String("task", "", "fanout task type (default: the template's task)")
		timeoutFlag  = fs.Int("timeout", 0, fmt.Sprintf("swarm execution timeout in seconds (default %d)", fanout.DefaultTimeoutSec))
		dryRunFlag   = fs.Bool("dry-run", false, "print the equivalent agy pro fanout command and exit 0")
	)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return SwarmFlags{}, 0
		}
		return SwarmFlags{}, 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(errw, SwarmUsage)
		return SwarmFlags{}, 2
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		for _, name := range SwarmFlagNames {
			if f.Name == name {
				set[name] = true
			}
		}
	})

	return SwarmFlags{
		Template:   *templateFlag,
		Seats:      *seatsFlag,
		Root:       *rootFlag,
		Prompt:     *promptFlag,
		PromptFile: *promptFile,
		Tools:      *toolsFlag,
		Task:       *taskFlag,
		Timeout:    *timeoutFlag,
		DryRun:     *dryRunFlag,
		Set:        set,
	}, 0
}

// ApplyToTemplate layers the --task / --tools overrides onto a template (A2).
// These are the flag equivalents of the interactive builder's edits: builder.go
// editField case 1 rewrites Template.Task and case 2 rewrites Template.Tools,
// and BuildFanoutArgs already emits both as `--task` / `--tools`. An empty or
// unset flag leaves the template default in place.
func (f SwarmFlags) ApplyToTemplate(t Template) Template {
	if task := strings.TrimSpace(f.Task); task != "" {
		t.Task = task
	}
	if tools := strings.TrimSpace(f.Tools); tools != "" {
		t.Tools = tools
	}
	return t
}

// ResolveTemplate resolves --template and layers the --task / --tools
// overrides onto it. It reports false for an unknown template name.
func (f SwarmFlags) ResolveTemplate() (Template, bool) {
	t, ok := FindTemplate(f.Template)
	if !ok {
		return Template{}, false
	}
	return f.ApplyToTemplate(t), true
}

// ValidateTools parses a --tools value fail-closed (S-8): the swarm surface
// uses the very same parser as the fanout CLI, so an unknown token is an error
// and never a substring match ("nowrite" must not enable write_file).
func ValidateTools(spec string) (string, error) {
	return fanout.ParseToolTokens(spec)
}

// EffectiveRoot returns the workspace root with the documented "." default.
func (f SwarmFlags) EffectiveRoot() string {
	if strings.TrimSpace(f.Root) == "" {
		return "."
	}
	return f.Root
}
