package cli

import (
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

const usageTemplate = `USAGE
  {{.UseLine}}
{{if .Aliases}}
ALIASES
{{range .Aliases}}  {{.}}
{{end}}{{end}}{{if .HasAvailableSubCommands}}
COMMANDS
{{range .Commands}}{{if .IsAvailableCommand}}  {{rpad .Name .NamePadding}} {{.Short}}
{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}
FLAGS
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}
{{end}}{{if .HasAvailableInheritedFlags}}
GLOBAL FLAGS
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}
{{end}}{{if .HasExample}}
EXAMPLES
{{.Example | trimTrailingWhitespaces | colorExamples}}
{{end}}{{if .HasAvailableSubCommands}}
LEARN MORE
  Use ` + "`{{.CommandPath}} <command> --help`" + ` for details on a command
{{end}}
`

func colorExamples(s string) string {
	prompt := color.New(color.Faint)
	command := color.New(color.FgCyan)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		rest, ok := strings.CutPrefix(trimmed, "$ ")
		if !ok {
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		lines[i] = indent + prompt.Sprint("$ ") + command.Sprint(rest)
	}
	return strings.Join(lines, "\n")
}

func installHelpStyle(cmd *cobra.Command) {
	cobra.AddTemplateFunc("colorExamples", colorExamples)
	cmd.SetUsageTemplate(usageTemplate)
	cmd.CompletionOptions.HiddenDefaultCmd = true
	cmd.InitDefaultHelpCmd()
	for _, sub := range cmd.Commands() {
		if sub.Name() == "help" {
			sub.Hidden = true
		}
	}
}
