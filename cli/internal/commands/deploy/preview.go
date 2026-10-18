package deploy

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/deploy"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

const defaultPreviewPruneKeepN = 3

type previewUpFlags struct {
	opts       deploy.Options
	persistent bool
}

func NewPreviewCommand(dependencies deploy.Dependencies) *cobra.Command {
	var up previewUpFlags

	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Deploy a preview of the current branch",
		Long: "Deploy a preview of the current branch.\n\n" +
			"`ocel preview` on its own is `ocel preview up`: it deploys the current branch's preview. " +
			"To address a preview by name, pass the name to a subcommand, as in `ocel preview up <name>`.\n\n" +
			"A preview is a full deployment beside production, torn down without touching anything else.",
		Example: "  $ ocel preview\n" +
			"  $ ocel preview up pr-12\n" +
			"  $ ocel preview up staging --persistent\n" +
			"  $ ocel preview ls\n" +
			"  $ ocel preview rm pr-12",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("`ocel preview` takes no name; to deploy the preview %q, run `ocel preview up %s`", args[0], args[0])
			}
			return nil
		},
		RunE: previewUpRunE(dependencies, &up),
	}
	addPreviewUpFlags(cmd, &up)

	upCmd := &cobra.Command{
		Use:   "up [name]",
		Short: "Deploy or refresh a preview",
		Long: "Deploy or refresh a preview.\n\n" +
			"Without a name the preview is the current branch's: deploying the same branch again replaces it, " +
			"and `ocel preview rm` tears it down. A name addresses one preview from any branch. " +
			"A name is a DNS label with no `--`.\n\n" +
			"A preview provisions the resources it declares in infrastructure of its own, and by default it is " +
			"ephemeral: torn down without a question. --persistent deploys one that asks before it is torn down " +
			"— a staging environment. A preview keeps the lifecycle it was created with.\n\n" +
			"--dry builds, then prints every change the preview would make to your account and stops.",
		Example: "  $ ocel preview up\n" +
			"  $ ocel preview up pr-12\n" +
			"  $ ocel preview up staging --persistent\n" +
			"  $ ocel preview up --dry",
		Args: cobra.MaximumNArgs(1),
		RunE: previewUpRunE(dependencies, &up),
	}
	addPreviewUpFlags(upCmd, &up)

	var removeYes bool
	rm := &cobra.Command{
		Use:   "rm [name]",
		Short: "Tear down a preview",
		Long: "Tear down a preview.\n\n" +
			"Without a name it takes down the current branch's preview. A persistent preview asks for confirmation first; " +
			"--yes skips that, and without a terminal to ask on it is required.",
		Example: "  $ ocel preview rm\n" +
			"  $ ocel preview rm pr-12\n" +
			"  $ ocel preview rm staging --yes",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return deploy.RemovePreview(cmd.Context(), dependencies, cwd, readPreviewNameArgument(args), removeYes, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	commands.AddYesFlag(rm, &removeYes)

	ls := &cobra.Command{
		Use:     "ls",
		Short:   "List this project's previews",
		Example: "  $ ocel preview ls",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return deploy.ListPreviews(cmd.Context(), dependencies, cwd, cmd.OutOrStdout())
		},
	}

	var pruneKeep int
	var pruneYes bool
	prune := &cobra.Command{
		Use:   "prune [name]",
		Short: "Delete a preview's old deployments",
		Long: "Delete a preview's old deployments.\n\n" +
			"Keeps the newest --keep deployments and whatever is live. Without a name it prunes the " +
			"current branch's preview.",
		Example: "  $ ocel preview prune\n" +
			"  $ ocel preview prune staging --keep 5",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return deploy.PrunePreview(cmd.Context(), dependencies, cwd, readPreviewNameArgument(args), pruneKeep)
		},
	}
	prune.Flags().IntVar(&pruneKeep, "keep", defaultPreviewPruneKeepN, "How many recent deployments to keep (the live one always stays)")
	commands.AddYesFlag(prune, &pruneYes)

	cmd.AddCommand(
		commands.DeclareRunEvents(commands.DeclareMutating(upCmd)),
		commands.DeclareRunEvents(commands.DeclareMutating(rm)),
		commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(ls)), &resultv1.PreviewListResult{}),
		commands.DeclareRunEvents(commands.DeclareMutating(prune)),
	)
	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}

func addPreviewUpFlags(cmd *cobra.Command, up *previewUpFlags) {
	cmd.Flags().BoolVar(&up.persistent, "persistent", false, "Deploy a persistent preview: a confirmation before it is torn down")
	cmd.Flags().BoolVar(&up.opts.Prebuilt, "prebuilt", false, prebuiltFlagUsage)
	commands.AddDryFlag(cmd, &up.opts.Dry, dryFlagUsage)
	commands.AddYesFlag(cmd, &up.opts.Yes)
}

func previewUpRunE(dependencies deploy.Dependencies, up *previewUpFlags) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		target := deploy.Preview(readPreviewNameArgument(args), up.persistent)
		return deploy.To(cmd.Context(), dependencies, cwd, target, up.opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	}
}

func readPreviewNameArgument(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
