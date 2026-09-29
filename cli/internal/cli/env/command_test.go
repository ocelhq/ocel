package env

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
)

func TestCommandHelp(t *testing.T) {
	cmd := NewCommand(clitest.NewDeps())
	if got := cmd.Use; got != "env <command>" {
		t.Errorf("Use = %q, want %q", got, "env <command>")
	}
	if got := cmd.Short; got != "Manage this project's variable values" {
		t.Errorf("Short = %q", got)
	}
	if got := cmd.Example; got != "  $ ocel env ls\n  $ ocel env set LOG_LEVEL=debug\n  $ ocel env ui --preview" {
		t.Errorf("Example = %q", got)
	}

	for _, name := range []string{"ls", "set", "get", "rm", "ref", "refs", "history", "ui"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if !strings.Contains(sub.Example, "$ ocel env "+name) {
			t.Errorf("%s Example = %q", name, sub.Example)
		}
	}
}

func TestCommandNeedsSubcommand(t *testing.T) {
	cmd := NewCommand(clitest.NewDeps())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	var exitErr *exitcode.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("Execute() = %v, want exit code 1", err)
	}
	if !strings.Contains(out.String(), "Usage:\n  env <command>") {
		t.Errorf("help = %q", out.String())
	}
}

func TestCommandFlags(t *testing.T) {
	cmd := NewCommand(clitest.NewDeps())
	cases := map[string]map[string]string{
		"ls":  {"preview": "Use preview values"},
		"set": {"preview": "Use preview values", "folder": "Use the value in this `folder`", "environment": "Use this named preview `environment`; requires --preview"},
		"get": {"preview": "Use preview values", "folder": "Use the value in this `folder`", "environment": "Use this named preview `environment`; requires --preview", "reveal": "Print the value"},
		"ref": {"target-project": "Read from another `project`", "target-folder": "Read from a `folder` in the target project", "target-key": "Read a different target `key`"},
	}
	for name, flags := range cases {
		sub, _, err := cmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		for flag, usage := range flags {
			got := sub.Flags().Lookup(flag)
			if got == nil || got.Usage != usage {
				t.Errorf("%s --%s = %#v, want usage %q", name, flag, got, usage)
			}
		}
	}
}

func TestNoEnvCommandAddressesDevValues(t *testing.T) {
	cmd := NewCommand(clitest.NewDeps())
	for _, name := range []string{"ls", "set", "get", "rm"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if flag := sub.Flags().Lookup("dev"); flag != nil {
			t.Errorf("%s --dev = %#v, want no such flag: dev values live in the dev tier's env source and .env.local", name, flag)
		}
	}
}

func TestCommandArguments(t *testing.T) {
	cmd := NewCommand(clitest.NewDeps())
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "ls", args: []string{"ls", "extra"}},
		{name: "set", args: []string{"set", "KEY"}},
		{name: "get", args: []string{"get"}},
		{name: "rm", args: []string{"rm"}},
		{name: "ref", args: []string{"ref"}},
		{name: "refs", args: []string{"refs"}},
		{name: "history", args: []string{"history"}},
		{name: "ui", args: []string{"ui", "extra"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd.SetArgs(test.args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("Execute() = nil")
			}
		})
	}
}

func TestParseEnvSetPairs(t *testing.T) {
	pairs, err := parseEnvSetPairs([]string{"LOG_LEVEL=debug", "DATABASE_URL=postgres://db?sslmode=require", "EMPTY="})
	if err != nil {
		t.Fatalf("parseEnvSetPairs() = %v", err)
	}
	want := []envSetPair{
		{key: "LOG_LEVEL", value: "debug"},
		{key: "DATABASE_URL", value: "postgres://db?sslmode=require"},
		{key: "EMPTY", value: ""},
	}
	if !slices.Equal(pairs, want) {
		t.Errorf("parseEnvSetPairs() = %#v, want %#v", pairs, want)
	}

	for _, arg := range []string{"LOG_LEVEL", "=debug"} {
		if _, err := parseEnvSetPairs([]string{arg}); err == nil {
			t.Errorf("parseEnvSetPairs(%q) = nil, want an error", arg)
		}
	}
}

func TestAnArgumentEnvSetCannotReadIsRefusedByPositionWithoutEchoingIt(t *testing.T) {
	_, err := parseEnvSetPairs([]string{"LOG_LEVEL=debug", "sk_live_pasted_without_a_key", "=hunter2"})
	if err == nil {
		t.Fatal("parseEnvSetPairs = nil, want an error")
	}
	if strings.Contains(err.Error(), "sk_live_pasted_without_a_key") {
		t.Errorf("parseEnvSetPairs = %q, echoes the argument it refused", err)
	}
	if !strings.Contains(err.Error(), "argument 2") {
		t.Errorf("parseEnvSetPairs = %q, want it to name the argument by position", err)
	}

	_, err = parseEnvSetPairs([]string{"=hunter2"})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("parseEnvSetPairs(=VALUE) = %v, want a refusal that echoes no value", err)
	}
}
