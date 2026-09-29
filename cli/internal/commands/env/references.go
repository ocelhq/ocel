package env

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func runEnvRef(ctx context.Context, dependencies Dependencies, cwd, key string, opts envOptions, ref envRefOptions, stdout, stderr io.Writer) error {
	for _, folder := range []string{opts.folder, ref.folder} {
		if folder == "" {
			continue
		}
		if err := variables.ValidateFolder(folder); err != nil {
			return err
		}
	}
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env ref", stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, status *contractv1.PreflightResponse) error {
		definitions, _, err := declaredVariables(ctx, dependencies, cfg, provider, key, opts, run)
		if err != nil {
			return err
		}
		if err := variables.RefuseUnwritable(definitions, key, opts.folder); err != nil {
			return err
		}
		target := ref.target(cfg.Slug, key)
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		resp, err := variableStore.SetReference(ctx, &variablestorev1.SetReferenceRequest{
			Tier:       opts.tier(),
			Coordinate: wireCoordinate(cfg.Slug, key, opts),
			Target:     target,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s now reads %s (version %d).\n", describeCell(key, opts), describeCoordinate(target), resp.GetMetadata().GetVersion())
		return nil
	})
}

func runEnvRefs(ctx context.Context, dependencies Dependencies, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env refs", stderr, func(ctx context.Context, _ *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		resp, err := variableStore.ListReferences(ctx, &variablestorev1.ListReferencesRequest{
			Tier:       opts.tier(),
			Coordinate: wireCoordinate(cfg.Slug, key, opts),
		})
		if err != nil {
			return err
		}
		renderReferences(stdout, describeCell(key, opts), resp.GetReferences())
		return nil
	})
}

func renderReferences(stdout io.Writer, cell string, references []*variablestorev1.Coordinate) {
	if len(references) == 0 {
		fmt.Fprintf(stdout, "Nothing references %s.\n", cell)
		return
	}
	fmt.Fprintf(stdout, "%d referencing %s:\n", len(references), cell)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tKEY\tFOLDER\tENVIRONMENT")
	for _, c := range references {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			c.GetSlug(), c.GetKey(), folderOrRoot(c.GetFolder()), environmentOrAll(c.GetEnvironment()))
	}
	_ = tw.Flush()
	fmt.Fprintln(stdout, "\nEditing this value changes what every one of them reads.")
}
