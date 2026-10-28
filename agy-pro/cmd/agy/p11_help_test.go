package main

import (
	"bytes"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/swarm"
)

func TestProFlagHelpExitsZeroAndBadFlagsExitTwo(t *testing.T) {
	commands := []struct {
		name  string
		usage string
		run   func([]string, *bytes.Buffer, *bytes.Buffer) int
	}{
		{"fanout", "Usage: agy pro fanout", func(args []string, out, errw *bytes.Buffer) int {
			return fanout.Run(args, strings.NewReader(""), out, errw)
		}},
		{"swarm", "Usage: agy pro swarm", func(args []string, out, errw *bytes.Buffer) int {
			return runSwarmCmd(args, strings.NewReader(""), out, errw)
		}},
		{"models", "Usage: agy pro models", func(args []string, out, errw *bytes.Buffer) int {
			return runModelsCmdTo(args, out, errw)
		}},
		{"code", "Usage: agy pro code", func(args []string, out, errw *bytes.Buffer) int {
			return runCodeCmd(args, strings.NewReader(""), out, errw)
		}},
		{"code plan", "Usage: agy pro code", func(args []string, out, errw *bytes.Buffer) int {
			return runCodeCmd(append([]string{"plan"}, args...), strings.NewReader(""), out, errw)
		}},
		{"code write", "Usage: agy pro code", func(args []string, out, errw *bytes.Buffer) int {
			return runCodeCmd(append([]string{"write"}, args...), strings.NewReader(""), out, errw)
		}},
		{"code review", "Usage: agy pro code", func(args []string, out, errw *bytes.Buffer) int {
			return runCodeCmd(append([]string{"review"}, args...), strings.NewReader(""), out, errw)
		}},
		{"code rollback", "Usage: agy pro code", func(args []string, out, errw *bytes.Buffer) int {
			return runCodeCmd(append([]string{"rollback"}, args...), strings.NewReader(""), out, errw)
		}},
		{"code run", "Usage: agy pro code", func(args []string, out, errw *bytes.Buffer) int {
			return runCodeCmd(append([]string{"run"}, args...), strings.NewReader(""), out, errw)
		}},
	}

	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			for _, help := range []string{"--help", "-h"} {
				var out, errw bytes.Buffer
				if code := command.run([]string{help}, &out, &errw); code != 0 {
					t.Errorf("%s exit = %d, want 0; stderr=%q", help, code, errw.String())
				}
				if combined := out.String() + errw.String(); !strings.Contains(combined, command.usage) {
					t.Errorf("%s output missing %q: %q", help, command.usage, combined)
				}
			}

			var out, errw bytes.Buffer
			if code := command.run([]string{"--definitely-invalid"}, &out, &errw); code != 2 {
				t.Errorf("bad flag exit = %d, want 2", code)
			}
			if errw.Len() == 0 {
				t.Error("bad flag printed no error")
			}
		})
	}
}

func TestParseSwarmFlagsHelpExitsZero(t *testing.T) {
	for _, help := range []string{"--help", "-h"} {
		var errw bytes.Buffer
		_, code := swarm.ParseSwarmFlags([]string{help}, &errw)
		if code != 0 {
			t.Errorf("%s exit = %d, want 0", help, code)
		}
		if !strings.Contains(errw.String(), "Usage: agy pro swarm") {
			t.Errorf("%s output missing usage: %q", help, errw.String())
		}
	}
}
