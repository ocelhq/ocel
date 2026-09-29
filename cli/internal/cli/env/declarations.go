package env

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/declcache"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func declaredVariables(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, prov *providerclient.Provider, key string, opts envOptions, run *events.Run) ([]*resourcesv1.VariableDefinition, []*resourcesv1.GroupDefinition, error) {
	prepared, err := deploycollector.Prepare(cfg)
	if err != nil {
		return nil, nil, err
	}
	fingerprint := prepared.Fingerprint()

	cache, cacheErr := declcache.Open()
	if cacheErr == nil {
		if definitions, groups, ok := cache.LoadContaining(cfg.Dir, fingerprint, key); ok {
			return withImpliedDeclarations(cfg, opts, definitions, groups)
		}
	}

	declarations := projectDeclarations(cfg, prov, opts)
	err = collecting(run, cfg, func(output io.Writer) error {
		_, err := deploycollector.Collect(ctx, cfg, declarations, prepared, io.Discard, output)
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

func collecting(run *events.Run, cfg *projectconfig.Config, collect func(output io.Writer) error) error {
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	unit := build.Unit(cfg.Slug, "Collecting the variables this project declares")
	err := collect(unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDERR))
	build.End(err)
	return err
}

func withImpliedDeclarations(cfg *projectconfig.Config, opts envOptions, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition) ([]*resourcesv1.VariableDefinition, []*resourcesv1.GroupDefinition, error) {
	implied, impliedGroups := variables.ImpliedDeclarations(variablescope.Of(cfg, opts.tier(), ""))
	return append(definitions, implied...), append(groups, impliedGroups...), nil
}

func discoverVariables(ctx context.Context, cfg *projectconfig.Config, prov *providerclient.Provider, opts envOptions, run *events.Run) (*variables.Declarations, error) {
	declarations := projectDeclarations(cfg, prov, opts)
	err := collecting(run, cfg, func(output io.Writer) error {
		_, err := deploycollector.PrepareAndCollect(ctx, cfg, declarations, io.Discard, output)
		return err
	})
	if err != nil {
		return nil, err
	}
	return declarations, nil
}

func projectDeclarations(cfg *projectconfig.Config, prov *providerclient.Provider, opts envOptions) *variables.Declarations {
	return variables.NewDeclarations(valuestore.Store{
		Provider: prov,
		Config:   cfg,
		Tier:     opts.tier(),
	}, variablescope.Of(cfg, opts.tier(), ""))
}
