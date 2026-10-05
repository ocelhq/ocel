package schema_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/commands/schema"
)

func newRootWith(subcommands ...*cobra.Command) *cobra.Command {
	root := &cobra.Command{Use: "ocel", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(subcommands...)
	root.AddCommand(schema.NewCommand(commands.Invocation{}))
	return root
}

func runSchema(root *cobra.Command, args ...string) (stdout string, err error) {
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"schema"}, args...))
	err = root.Execute()
	return out.String(), err
}

func requireUsageError(t *testing.T, err error, want string) {
	t.Helper()
	var failure *clierror.Error
	if !errors.As(err, &failure) || failure.Code != clierror.CodeUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(failure.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", failure.Error(), want)
	}
}

func noop(*cobra.Command, []string) error { return nil }

func TestSchemaOfACommandThatDeclaresNeitherAResultNorRunEventsFailsWithUsage(t *testing.T) {
	root := newRootWith(&cobra.Command{Use: "plain", RunE: noop})

	stdout, err := runSchema(root, "plain")

	requireUsageError(t, err, "has no schema")
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestSchemaOfACommandUnderAHiddenCommandFailsWithUsage(t *testing.T) {
	hidden := &cobra.Command{Use: "completion", Hidden: true}
	hidden.AddCommand(commands.DeclareRunEvents(&cobra.Command{Use: "bash", RunE: noop}))
	root := newRootWith(hidden)

	for _, path := range [][]string{{"completion", "bash"}, {"completion"}} {
		stdout, err := runSchema(root, path...)

		requireUsageError(t, err, strings.Join(path, " "))
		if stdout != "" {
			t.Errorf("ocel schema %s: stdout = %q, want nothing", strings.Join(path, " "), stdout)
		}
	}
}

func TestSchemaOfACommandDeclaringRunEventsPrintsTheRunEventSchema(t *testing.T) {
	root := newRootWith(commands.DeclareRunEvents(&cobra.Command{Use: "deploy", RunE: noop}))

	stdout, err := runSchema(root, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"cli.stream.v1.RunEvent.jsonschema.json"`) {
		t.Errorf("stdout = %.200q, want the run event schema", stdout)
	}
}

func TestSchemaOfAnUnknownCommandKeepsWhyItWasNotFound(t *testing.T) {
	root := newRootWith(&cobra.Command{Use: "env"}, &cobra.Command{Use: "deploy", RunE: noop})

	_, err := runSchema(root, "deplyo")

	requireUsageError(t, err, `"deplyo"`)
	requireUsageError(t, err, "Did you mean this?\n\tdeploy")
}
