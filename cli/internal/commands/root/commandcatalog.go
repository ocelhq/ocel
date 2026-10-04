package root

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/commands"
	helpv1 "github.com/ocelhq/ocel/pkg/proto/cli/help/v1"
)

const cobraHelpFlag = "help"

func describeCommandCatalog(start *cobra.Command) *helpv1.CommandCatalog {
	catalog := &helpv1.CommandCatalog{}
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Hidden {
			return
		}
		if cmd.HasParent() {
			catalog.Commands = append(catalog.Commands, describeCommand(cmd))
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(start)
	sort.Slice(catalog.Commands, func(i, j int) bool { return catalog.Commands[i].GetPath() < catalog.Commands[j].GetPath() })
	return catalog
}

func describeCommand(cmd *cobra.Command) *helpv1.Command {
	mutates, _ := commands.FindMutation(cmd)
	return &helpv1.Command{
		Path:       commandPath(cmd),
		Short:      cmd.Short,
		Long:       cmd.Long,
		Usage:      cmd.UseLine(),
		Arguments:  describeArguments(cmd),
		Aliases:    cmd.Aliases,
		Flags:      describeFlags(cmd),
		PrintsData: commands.PrintsData(cmd),
		Mutates:    mutates,
		Confirms:   commands.HasConfirmationFlag(cmd),
	}
}

func commandPath(cmd *cobra.Command) string {
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

func describeArguments(cmd *cobra.Command) []*helpv1.Argument {
	if cmd.HasSubCommands() {
		return nil
	}
	var arguments []*helpv1.Argument
	for _, token := range strings.Fields(cmd.Use)[1:] {
		required := strings.HasPrefix(token, "<")
		if !required && !strings.HasPrefix(token, "[") {
			continue
		}
		variadic := strings.Contains(token, "...")
		name := strings.Trim(strings.ReplaceAll(token, "...", ""), "<>[]")
		if name == "flags" {
			continue
		}
		arguments = append(arguments, &helpv1.Argument{Name: name, Required: required, Variadic: variadic})
	}
	return arguments
}

func describeFlags(cmd *cobra.Command) []*helpv1.Flag {
	var flags []*helpv1.Flag
	collect := func(set *pflag.FlagSet, persistent bool) {
		set.VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden || flag.Name == cobraHelpFlag {
				return
			}
			_, description := pflag.UnquoteUsage(flag)
			flags = append(flags, &helpv1.Flag{
				Name:        flag.Name,
				Shorthand:   flag.Shorthand,
				Type:        flag.Value.Type(),
				Default:     flag.DefValue,
				Description: description,
				Persistent:  persistent,
			})
		})
	}
	collect(cmd.LocalNonPersistentFlags(), false)
	collect(cmd.PersistentFlags(), true)
	collect(cmd.InheritedFlags(), true)
	sort.Slice(flags, func(i, j int) bool { return flags[i].GetName() < flags[j].GetName() })
	return flags
}

func writeCommandCatalog(w io.Writer, catalog *helpv1.CommandCatalog) error {
	compact, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("render the command catalog: %w", err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact, "", "  "); err != nil {
		return fmt.Errorf("render the command catalog: %w", err)
	}
	indented.WriteByte('\n')
	_, err = w.Write(indented.Bytes())
	return err
}
