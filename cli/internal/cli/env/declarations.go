package env

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func declaredVariables(ctx context.Context, deps cmddeps.Deps, cfg *project.Project, prov *providerclient.Provider, key string, opts envOptions, run *run.Run) ([]*resourcesv1.VariableDefinition, []*resourcesv1.GroupDefinition, error) {
	prepared, err := declaration.Prepare(cfg)
	if err != nil {
		return nil, nil, err
	}
	var fingerprint string
	if prepared.Entry() != "" {
		if fingerprint, err = hashBundledEntry(prepared.Entry()); err != nil {
			return nil, nil, err
		}
	}

	cache, cacheErr := openVariableCache()
	if cacheErr == nil {
		if definitions, groups, ok := cache.LoadContaining(cfg.Dir, fingerprint, key); ok {
			return withImpliedDeclarations(cfg, opts, definitions, groups)
		}
	}

	declarations := projectDeclarations(cfg, prov, opts)
	err = collecting(run, cfg, func(output io.Writer) error {
		_, err := declaration.CollectPrepared(ctx, cfg, declarations, prepared, io.Discard, output)
		return err
	})
	if err != nil {
		return nil, nil, err
	}

	definitions, groups := declarations.Definitions(), declarations.Groups()
	if cacheErr == nil {
		_ = cache.Save(cfg.Dir, fingerprint, definitions, groups)
	}
	return withImpliedDeclarations(cfg, opts, definitions, groups)
}

func collecting(run *run.Run, cfg *project.Project, collect func(output io.Writer) error) error {
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	unit := build.Unit(cfg.Slug, progress.Collecting.Title("the variables this project declares"))
	err := collect(unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDERR))
	build.End(err)
	return err
}

func withImpliedDeclarations(cfg *project.Project, opts envOptions, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition) ([]*resourcesv1.VariableDefinition, []*resourcesv1.GroupDefinition, error) {
	implied, impliedGroups := variables.ImpliedDeclarations(variablescope.Of(cfg, opts.tier(), ""))
	return append(definitions, implied...), append(groups, impliedGroups...), nil
}

func discoverVariables(ctx context.Context, cfg *project.Project, prov *providerclient.Provider, opts envOptions, run *run.Run) (*variables.Declarations, error) {
	declarations := projectDeclarations(cfg, prov, opts)
	err := collecting(run, cfg, func(output io.Writer) error {
		_, err := declaration.Collect(ctx, cfg, declarations, io.Discard, output)
		return err
	})
	if err != nil {
		return nil, err
	}
	return declarations, nil
}

func projectDeclarations(cfg *project.Project, prov *providerclient.Provider, opts envOptions) *variables.Declarations {
	return variables.NewDeclarations(valuestore.Store{
		Provider: prov,
		Project:  cfg,
		Tier:     opts.tier(),
	}, variablescope.Of(cfg, opts.tier(), ""))
}
