package cli

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/console"
)

const usageTemplate = `{{heading "USAGE"}}
  {{.UseLine}}
{{if .Aliases}}
{{heading "ALIASES"}}
{{range .Aliases}}  {{.}}
{{end}}{{end}}{{if .HasAvailableSubCommands}}{{range commandGroups .}}
{{heading .Title}}
{{range .Commands}}  {{rpad .Name .NamePadding}}  {{.Short}}
{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}
{{heading "FLAGS"}}
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}
{{end}}{{if .HasAvailableInheritedFlags}}
{{heading "GLOBAL FLAGS"}}
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}
{{end}}{{with index .Annotations "environment"}}
{{heading "ENVIRONMENT"}}
{{.}}
{{end}}{{if .HasExample}}
{{heading "EXAMPLES"}}
{{.Example | trimTrailingWhitespaces | colorExamples}}
{{end}}{{if .HasAvailableSubCommands}}
{{heading "LEARN MORE"}}
  Use ` + "`{{.CommandPath}} <command> --help`" + ` for details on a command
{{end}}
`

const (
	coreGroup     = "core"
	consoleGroup  = "console"
	envAnnotation = "environment"
)

var (
	heading = color.New(color.Bold).SprintFunc()
	envName = color.New(color.FgCyan).SprintFunc()
)

var consoleEnv = fmt.Sprintf("  %s  Console to talk to; set it to use a self-hosted one\n  %*s  Default: the console you logged in to, else %s",
	envName(console.URLEnvVar), len(console.URLEnvVar), "", console.DefaultBaseURL)

type commandGroup struct {
	Title    string
	Commands []*cobra.Command
}

func commandGroups(cmd *cobra.Command) []commandGroup {
	if len(cmd.Groups()) == 0 {
		return []commandGroup{{Title: "COMMANDS", Commands: availableCommands(cmd, func(*cobra.Command) bool { return true })}}
	}
	var groups []commandGroup
	for _, g := range cmd.Groups() {
		commands := availableCommands(cmd, func(sub *cobra.Command) bool {
			return sub.GroupID == g.ID || (sub.GroupID == "" && g.ID == coreGroup)
		})
		if len(commands) > 0 {
			groups = append(groups, commandGroup{Title: g.Title, Commands: commands})
		}
	}
	return groups
}

func availableCommands(cmd *cobra.Command, keep func(*cobra.Command) bool) []*cobra.Command {
	var commands []*cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.IsAvailableCommand() && keep(sub) {
			commands = append(commands, sub)
		}
	}
	return commands
}

func addConsoleCommands(root *cobra.Command, cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		cmd.GroupID = consoleGroup
		root.AddCommand(cmd)
	}
}

func readsConsoleURL(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[envAnnotation] = consoleEnv
	}
}

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
	cobra.AddTemplateFunc("heading", heading)
	cobra.AddTemplateFunc("commandGroups", commandGroups)
	cmd.SetUsageTemplate(usageTemplate)
	cmd.CompletionOptions.HiddenDefaultCmd = true
	cmd.InitDefaultHelpCmd()
	for _, sub := range cmd.Commands() {
		if sub.Name() == "help" {
			sub.Hidden = true
		}
	}
}
