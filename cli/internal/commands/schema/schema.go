package schema

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/outputschema"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func NewCommand(invocation commands.Invocation) *cobra.Command {
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(&cobra.Command{
		Use:   "schema [command...]",
		Short: "Print the JSON Schema of what a command prints",
		Long: "Print the JSON Schema of what a command prints.\n\n" +
			"A command that returns data prints one result envelope under --json, and its schema " +
			"covers both the result and the failure document. A command that runs prints the " +
			"run-event stream, and its schema covers one line of it. Without a command, lists " +
			"every command that has a schema.",
		Example: "  $ ocel schema env ls\n  $ ocel schema deploy\n  $ ocel schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listSchemas(invocation, cmd)
			}
			return printSchema(cmd, args)
		},
	})), &resultv1.SchemaListResult{})
}

func findOutput(cmd *cobra.Command) resultv1.CommandOutput {
	switch {
	case hasResults(cmd):
		return resultv1.CommandOutput_COMMAND_OUTPUT_RESULT
	case commands.PrintsRunEvents(cmd):
		return resultv1.CommandOutput_COMMAND_OUTPUT_RUN_EVENTS
	default:
		return resultv1.CommandOutput_COMMAND_OUTPUT_UNSPECIFIED
	}
}

func hasResults(cmd *cobra.Command) bool {
	_, declared := commands.FindResults(cmd)
	return declared
}

func isHidden(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Hidden {
			return true
		}
	}
	return false
}

func formatPath(cmd *cobra.Command) string {
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

func listSchemas(invocation commands.Invocation, cmd *cobra.Command) error {
	list := &resultv1.SchemaListResult{}
	var walk func(parent *cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, sub := range parent.Commands() {
			if sub.Hidden {
				continue
			}
			if output := findOutput(sub); output != resultv1.CommandOutput_COMMAND_OUTPUT_UNSPECIFIED {
				list.Commands = append(list.Commands, &resultv1.CommandSchema{Path: formatPath(sub), Output: output})
			}
			walk(sub)
		}
	}
	walk(cmd.Root())
	sort.Slice(list.Commands, func(i, j int) bool { return list.Commands[i].GetPath() < list.Commands[j].GetPath() })

	stdout := cmd.OutOrStdout()
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, list)
	}
	writeSchemaList(stdout, list)
	return nil
}

func writeSchemaList(stdout io.Writer, list *resultv1.SchemaListResult) {
	width := 0
	for _, command := range list.GetCommands() {
		width = max(width, len(command.GetPath()))
	}
	for _, command := range list.GetCommands() {
		fmt.Fprintf(stdout, "%-*s  %s\n", width, command.GetPath(), describeOutput(command.GetOutput()))
	}
}

func describeOutput(output resultv1.CommandOutput) string {
	if output == resultv1.CommandOutput_COMMAND_OUTPUT_RUN_EVENTS {
		return "run-events"
	}
	return "result"
}

func printSchema(cmd *cobra.Command, args []string) error {
	path := strings.Join(args, " ")
	target, rest, err := cmd.Root().Find(args)
	if err != nil || len(rest) > 0 || isHidden(target) || target == cmd.Root() {
		return refuseUsage(cmd, fmt.Errorf("no command %q: `ocel schema` lists the commands that have a schema%s", path, suggestCommands(target, rest)))
	}
	var schema []byte
	switch findOutput(target) {
	case resultv1.CommandOutput_COMMAND_OUTPUT_RESULT:
		results, _ := commands.FindResults(target)
		schema, err = outputschema.Result(results...)
	case resultv1.CommandOutput_COMMAND_OUTPUT_RUN_EVENTS:
		schema, err = outputschema.RunEvent()
	default:
		return refuseUsage(cmd, fmt.Errorf("`ocel %s` has no schema: `ocel schema` lists the commands that do", formatPath(target)))
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", strings.TrimSpace(string(schema)))
	return err
}

func suggestCommands(found *cobra.Command, rest []string) string {
	if len(rest) == 0 {
		return ""
	}
	suggestions := found.SuggestionsFor(rest[0])
	if len(suggestions) == 0 {
		return ""
	}
	return "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t")
}

func refuseUsage(cmd *cobra.Command, cause error) error {
	return &clierror.Error{Code: clierror.CodeUsage, Hint: cmd.UseLine(), Cause: cause}
}
