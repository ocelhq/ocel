package skill

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/skill"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

type installOptions struct {
	yes    bool
	global bool
}

func NewCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage the ocel skill coding agents read",
		Long: "Manage the ocel skill coding agents read.\n\n" +
			"The skill teaches a coding agent how to declare resources with the ocel SDK, run " +
			"ocel commands, read their --json output and leave production to you.",
	}
	cmd.AddCommand(newInstallCommand(invocation))
	return commands.DeclareReadOnly(commands.ReserveStdout(cmd))
}

func newInstallCommand(invocation commands.Invocation) *cobra.Command {
	var opts installOptions
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write the ocel skill where coding agents look for it",
		Long: "Write the ocel skill where coding agents look for it.\n\n" +
			"Writes the skill this CLI carries into .claude/skills/ocel and .agents/skills/ocel at " +
			"the project root, or under your home directory with --global, replacing what those two " +
			"directories held. Its SKILL.md records this CLI's version, so `ocel doctor` can say when " +
			"it falls behind. Without --yes it lists the files it would write and writes nothing.",
		Example: "  $ ocel skill install\n  $ ocel skill install --yes\n  $ ocel skill install --global --yes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runInstall(invocation, opts, cwd, version.Version, cmd.OutOrStdout())
		},
	}
	commands.AddWriteYesFlag(cmd, &opts.yes, "Write the files; without it, list what would be written and write nothing")
	cmd.Flags().BoolVar(&opts.global, "global", false, "Write into ~/.claude/skills/ocel and ~/.agents/skills/ocel instead of the project")
	return commands.DeclareResult(commands.DeclareMutating(commands.ReserveStdout(cmd)), &resultv1.SkillInstallResult{})
}

func runInstall(invocation commands.Invocation, opts installOptions, cwd, cliVersion string, stdout io.Writer) error {
	base, err := installBase(invocation, opts, cwd)
	if err != nil {
		return err
	}
	dirs := skill.Dirs(base)
	if opts.yes {
		for _, dir := range dirs {
			if err := skill.Write(dir, cliVersion); err != nil {
				return err
			}
		}
	}
	result := &resultv1.SkillInstallResult{Written: opts.yes, OcelVersion: cliVersion}
	for _, dir := range dirs {
		result.Targets = append(result.Targets, &resultv1.SkillTarget{Dir: dir, Files: skill.ListFiles()})
	}
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, result)
	}
	printInstall(stdout, result, opts, invocation.ConfigPath())
	return nil
}

func installBase(invocation commands.Invocation, opts installOptions, cwd string) (string, error) {
	if opts.global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find your home directory: %w", err)
		}
		return home, nil
	}
	return project.FindRoot(cwd, invocation.ConfigPath())
}

func printInstall(stdout io.Writer, result *resultv1.SkillInstallResult, opts installOptions, configPath string) {
	verb := "Would write"
	if result.GetWritten() {
		verb = "Wrote"
	}
	fmt.Fprintf(stdout, "%s the ocel skill for ocel %s:\n", verb, result.GetOcelVersion())
	for _, target := range result.GetTargets() {
		for _, name := range target.GetFiles() {
			fmt.Fprintf(stdout, "  %s\n", filepath.Join(target.GetDir(), filepath.FromSlash(name)))
		}
	}
	if result.GetWritten() {
		return
	}
	again := "ocel skill install --yes"
	switch {
	case opts.global:
		again = "ocel skill install --global --yes"
	case configPath != "":
		again = "ocel skill install --config " + shellQuoted(configPath) + " --yes"
	}
	fmt.Fprintf(stdout, "Nothing was written. Run `%s` to write them.\n", again)
}

func shellQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
