package env

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func runEnvRef(ctx context.Context, dependencies Dependencies, cwd, key string, opts envOptions, ref envRefOptions, stdout, stderr io.Writer) error {
	for _, folder := range []string{opts.folder, ref.folder} {
		if folder == "" {
			continue
		}
		if err := project.ValidateFolder(folder); err != nil {
			return err
		}
	}
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env ref", []cloudv1.Scope{cloudv1.Scope_SCOPE_VARIABLES_WRITE}, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, status *contractv1.PreflightResponse) error {
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
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env refs", nil, stderr, func(ctx context.Context, _ *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		req := &variablestorev1.ListReferencesRequest{
			Tier:       opts.tier(),
			Coordinate: wireCoordinate(cfg.Slug, key, opts),
		}
		resp, err := variableStore.ListReferences(ctx, req)
		if err != nil {
			return err
		}
		present := dependencies.Presentation(stdout)
		if present.Format == terminal.FormatJSON {
			return writeEnvRefsJSON(stdout, req, resp.GetReferences())
		}
		renderReferences(stdout, present.Palette(), describeCell(key, opts), resp.GetReferences())
		return nil
	})
}

func writeEnvRefsJSON(stdout io.Writer, req *variablestorev1.ListReferencesRequest, references []*variablestorev1.Coordinate) error {
	result := &resultv1.EnvRefsResult{
		Tier:       req.GetTier(),
		Coordinate: newResultCoordinate(req.GetCoordinate()),
		References: make([]*resultv1.EnvCoordinate, 0, len(references)),
	}
	for _, reference := range references {
		result.References = append(result.References, newResultCoordinate(reference))
	}
	return terminal.WriteResultJSON(stdout, result)
}

func renderReferences(stdout io.Writer, palette terminal.Palette, cell string, references []*variablestorev1.Coordinate) {
	if len(references) == 0 {
		fmt.Fprintf(stdout, "Nothing references %s.\n", cell)
		return
	}
	fmt.Fprintf(stdout, "%s\n\n", palette.Heading(fmt.Sprintf("%d referencing %s", len(references), cell)))
	table := terminal.NewTable(
		terminal.Column{Head: "PROJECT"},
		terminal.Column{Head: "KEY"},
		terminal.Column{Head: "FOLDER"},
		terminal.Column{Head: "ENVIRONMENT"},
	)
	for _, c := range references {
		table.Row(c.GetSlug(), palette.Bold(c.GetKey()), folderOrRoot(c.GetFolder()), environmentOrAll(c.GetEnvironment()))
	}
	fmt.Fprint(stdout, table.Render(palette, "  "))
	fmt.Fprintln(stdout, "\n"+palette.Warning("Editing this value changes what every one of them reads."))
}
