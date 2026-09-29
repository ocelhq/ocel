package env

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func envValues(prov *providerclient.Provider, cfg *project.Project, opts envOptions) valuestore.Store {
	return valuestore.Store{Provider: prov, Project: cfg, Tier: opts.tier()}
}

func staleCell(err error, key string, opts envOptions) error {
	if !errors.Is(err, variables.ErrStaleValue) {
		return err
	}
	return fmt.Errorf("%s moved between this command reading it and writing it, so nothing was written — somebody else edited it at the same time. Read it again with `ocel env get %s` and run this command again",
		describeCell(key, opts), key)
}

func runEnvSet(ctx context.Context, dependencies Dependencies, cwd, key, value string, opts envOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	return runEnvSetPairs(ctx, dependencies, cwd, []envSetPair{{key: key, value: value}}, opts, stdin, stdout, stderr)
}

func runEnvSetPairs(ctx context.Context, dependencies Dependencies, cwd string, pairs []envSetPair, opts envOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	if opts.folder != "" {
		if err := variables.ValidateFolder(opts.folder); err != nil {
			return err
		}
	}
	key := ""
	if len(pairs) > 0 {
		key = pairs[0].key
	}
	return withEnvProviderOfferingVariablesKey(ctx, dependencies, cwd, opts, "ocel env set", stdin, stderr, func(ctx context.Context, run *run.Run, prov *providerclient.Provider, cfg *project.Project, status *contractv1.PreflightResponse) error {
		definitions, groups, err := declaredVariables(ctx, dependencies, cfg, prov, key, opts, run)
		if err != nil {
			return err
		}
		for _, pair := range pairs {
			if err := variables.RefuseImpliedInFolder(variablescope.Of(cfg, opts.tier(), ""), pair.key, opts.folder); err != nil {
				return err
			}
			if err := variables.RefuseUnwritable(definitions, pair.key, opts.folder); err != nil {
				return err
			}
		}
		vars, err := prov.Vars()
		if err != nil {
			return err
		}
		values := envValues(prov, cfg, opts)
		var owner variables.EnvSource
		if opts.environment == "" {
			if owner, err = values.DescribeEnvSource(ctx); err != nil {
				return err
			}
		}
		for _, pair := range pairs {
			at := envCoordinate(pair.key, opts)
			if owner.CanSet(at) {
				if err := setInEnvSource(ctx, values, owner.ID, at, pair.value, definitions, stdout); err != nil {
					return err
				}
				continue
			}
			seen, err := values.Version(ctx, at)
			if err != nil {
				return err
			}
			version, err := values.Set(ctx, at, pair.value, &seen)
			if err != nil {
				return staleCell(err, pair.key, opts)
			}
			fmt.Fprintf(stdout, "Set %s (version %d).\n", describeCell(pair.key, opts), version)
		}
		if err := printGroupProgress(ctx, vars, cfg.Slug, definitions, groups, opts, pairs, stdout); err != nil {
			return err
		}
		return nil
	})
}

func setInEnvSource(ctx context.Context, values valuestore.Store, id string, at variables.Coordinate, value string, definitions []*resourcesv1.VariableDefinition, stdout io.Writer) error {
	description := ""
	if i := slices.IndexFunc(definitions, func(definition *resourcesv1.VariableDefinition) bool { return definition.GetKey() == at.Cell.Key }); i >= 0 {
		description = definitions[i].GetDescription()
	}
	awaiting, err := values.SetInEnvSource(ctx, at.Cell, value, description)
	if err != nil {
		return err
	}
	where := at.Cell.Key
	if at.Cell.Folder != "" {
		where += " in " + at.Cell.Folder
	}
	if awaiting {
		fmt.Fprintf(stdout, "Sent %s to %s, where it waits for approval; ocel copies it once it is approved.\n", where, id)
		return nil
	}
	fmt.Fprintf(stdout, "Set %s in %s, and copied it back.\n", where, id)
	return nil
}

func runEnvGet(ctx context.Context, dependencies Dependencies, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env get", stderr, func(ctx context.Context, run *run.Run, prov *providerclient.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		definitions, _, err := declaredVariables(ctx, dependencies, cfg, prov, key, opts, run)
		if err != nil {
			return err
		}
		vars, err := prov.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.GetValue(ctx, &envvarsv1.GetValueRequest{
			Tier:       opts.tier(),
			Coordinate: wireCoordinate(cfg.Slug, key, opts),
			Reveal:     opts.reveal,
		})
		if err != nil {
			return err
		}
		if !resp.GetFound() {
			return fmt.Errorf("no value is set for %s; set one with `ocel env set %s=<VALUE>`%s", describeCell(key, opts), key, terminal.VariableDescriptionLine(descriptions(definitions)[key]))
		}

		if opts.reveal {
			if err := consentToReveal(definitions, key, opts, run.Phase(progressv1.Phase_PHASE_CHECK).Warn); err != nil {
				return err
			}
			fmt.Fprintln(stdout, resp.GetValue())
			return nil
		}
		m := resp.GetMetadata()
		if target := m.GetTarget(); target != nil {
			fmt.Fprintf(stdout, "%s references %s — version %d, pointed %s\n", describeCell(key, opts), describeCoordinate(target), m.GetVersion(), terminal.EpochDate(m.GetUpdatedAt()))
			fmt.Fprintln(stdout, "Pass --reveal to print the value it reads. Edit that value where it is set.")
			return nil
		}
		fmt.Fprintf(stdout, "%s — version %d, %d bytes, updated %s\n", describeCell(key, opts), m.GetVersion(), m.GetSize(), terminal.EpochDate(m.GetUpdatedAt()))
		fmt.Fprintln(stdout, "Pass --reveal to print the value.")
		return nil
	})
}

func classOf(definitions []*resourcesv1.VariableDefinition, key string) resourcesv1.VariableClass {
	for _, definition := range definitions {
		if definition.GetKey() == key {
			return definition.GetClass()
		}
	}
	return resourcesv1.VariableClass_VARIABLE_CLASS_UNSPECIFIED
}

func consentToReveal(definitions []*resourcesv1.VariableDefinition, key string, opts envOptions, warn func(message string)) error {
	if classOf(definitions, key) != resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
		return nil
	}
	if !opts.yes {
		return fmt.Errorf("%s is declared a secret, and --reveal alone will not print one: the plaintext would land in this terminal's scrollback and in whatever shell history, CI log or screen recording is watching. Pass --yes as well to print it anyway",
			describeCell(key, opts))
	}
	warn(describeCell(key, opts) + " is a secret and its plaintext is now on stdout, in this terminal's scrollback, and in anything capturing either.")
	return nil
}

func runEnvRemove(ctx context.Context, dependencies Dependencies, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env rm", stderr, func(ctx context.Context, run *run.Run, prov *providerclient.Provider, cfg *project.Project, status *contractv1.PreflightResponse) error {
		vars, err := prov.Vars()
		if err != nil {
			return err
		}
		values := envValues(prov, cfg, opts)
		at := envCoordinate(key, opts)
		seen, err := values.Version(ctx, at)
		if err != nil {
			return err
		}
		deleted, err := values.Delete(ctx, at, &seen)
		if err != nil {
			return staleCell(err, key, opts)
		}
		if !deleted {
			fmt.Fprintf(stdout, "No value was set for %s.\n", describeCell(key, opts))
			return nil
		}
		fmt.Fprintf(stdout, "Removed %s.\n", describeCell(key, opts))
		definitions, groups, err := declaredVariables(ctx, dependencies, cfg, prov, key, opts, run)
		if err != nil {
			return err
		}
		if err := printGroupProgress(ctx, vars, cfg.Slug, definitions, groups, opts, []envSetPair{{key: key}}, stdout); err != nil {
			return err
		}
		return nil
	})
}

func runEnvHistory(ctx context.Context, dependencies Dependencies, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env history", stderr, func(ctx context.Context, _ *run.Run, prov *providerclient.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		vars, err := prov.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.ListVersions(ctx, &envvarsv1.ListVersionsRequest{
			Tier:       opts.tier(),
			Coordinate: wireCoordinate(cfg.Slug, key, opts),
		})
		if err != nil {
			return err
		}
		renderVersions(stdout, describeCell(key, opts), resp.GetVersions())
		return nil
	})
}

func renderVersions(stdout io.Writer, cell string, versions []*envvarsv1.VersionEntry) {
	if len(versions) == 0 {
		fmt.Fprintf(stdout, "No history for %s.\n", cell)
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tCREATED\tBYTES")
	for _, v := range versions {
		fmt.Fprintf(tw, "%d\t%s\t%d\n", v.GetVersion(), terminal.EpochDate(v.GetCreatedAt()), v.GetSize())
	}
	_ = tw.Flush()
}
