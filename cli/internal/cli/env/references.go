package env

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/variables"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func runEnvRef(ctx context.Context, deps cmddeps.Deps, cwd, key string, opts envOptions, ref envRefOptions, stdout, stderr io.Writer) error {
	for _, folder := range []string{opts.folder, ref.folder} {
		if folder == "" {
			continue
		}
		if err := variables.ValidateFolder(folder); err != nil {
			return err
		}
	}
	return withEnvProvider(ctx, deps, cwd, opts, "ocel env ref", stderr, func(ctx context.Context, run *events.Run, prov *providerclient.Provider, cfg *projectconfig.Config, status *contractv1.PreflightResponse) error {
		definitions, _, err := declaredVariables(ctx, deps, cfg, prov, key, opts, run)
		if err != nil {
			return err
		}
		if err := variables.RefuseUnwritable(definitions, key, opts.folder); err != nil {
			return err
		}
		target := ref.target(cfg.Slug, key)
		vars, err := prov.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.SetReference(ctx, &envvarsv1.SetReferenceRequest{
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

func runEnvRefs(ctx context.Context, deps cmddeps.Deps, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, deps, cwd, opts, "ocel env refs", stderr, func(ctx context.Context, _ *events.Run, prov *providerclient.Provider, cfg *projectconfig.Config, _ *contractv1.PreflightResponse) error {
		vars, err := prov.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.ListReferences(ctx, &envvarsv1.ListReferencesRequest{
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

func renderReferences(stdout io.Writer, cell string, references []*envvarsv1.Coordinate) {
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
