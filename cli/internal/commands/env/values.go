package env

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variables/wording"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func envValues(provider *providerprocess.Provider, cfg *project.Project, opts envOptions) variables.Store {
	return variables.Store{Provider: provider, Project: cfg, Tier: opts.tier()}
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
		if err := project.ValidateFolder(opts.folder); err != nil {
			return err
		}
	}
	key := ""
	if len(pairs) > 0 {
		key = pairs[0].key
	}
	return withEnvProviderOfferingVariablesKey(ctx, dependencies, cwd, opts, "ocel env set", []cloudv1.Scope{cloudv1.Scope_SCOPE_VARIABLES_WRITE}, stdin, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, status *contractv1.PreflightResponse) error {
		definitions, groups, err := declaredVariables(ctx, dependencies, cfg, provider, key, opts, run)
		if err != nil {
			return err
		}
		for _, pair := range pairs {
			if err := variables.RefuseImpliedInFolder(variables.ScopeOf(cfg, opts.tier(), ""), pair.key, opts.folder); err != nil {
				return err
			}
			if err := variables.RefuseUnwritable(definitions, pair.key, opts.folder); err != nil {
				return err
			}
		}
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(pairs))
		for _, pair := range pairs {
			keys = append(keys, pair.key)
		}
		err = runInSpan(run, progressv1.Phase_PHASE_PROVISION, cfg, progress.Updating.Title(describeValues(keys, opts)), func() error {
			return setPairs(ctx, envValues(provider, cfg, opts), pairs, opts, definitions, stdout)
		})
		if err != nil {
			return err
		}
		return printGroupProgress(ctx, run, variableStore, cfg, definitions, groups, opts, pairs, stdout)
	})
}

func setPairs(ctx context.Context, values variables.Store, pairs []envSetPair, opts envOptions, definitions []*resourcesv1.VariableDefinition, stdout io.Writer) error {
	var owner variables.EnvSource
	if opts.environment == "" {
		var err error
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
	return nil
}

func setInEnvSource(ctx context.Context, values variables.Store, id string, at variables.Coordinate, value string, definitions []*resourcesv1.VariableDefinition, stdout io.Writer) error {
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
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env get", revealScopes(opts), stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		definitions, _, err := declaredVariables(ctx, dependencies, cfg, provider, key, opts, run)
		if err != nil {
			return err
		}
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		req := &variablestorev1.GetValueRequest{
			Tier:       opts.tier(),
			Coordinate: wireCoordinate(cfg.Slug, key, opts),
			Reveal:     opts.reveal,
		}
		var resp *variablestorev1.GetValueResponse
		err = runInSpan(run, progressv1.Phase_PHASE_CHECK, cfg, progress.Reading.Title(describeValues([]string{key}, opts)), func() (err error) {
			resp, err = variableStore.GetValue(ctx, req)
			return err
		})
		if err != nil {
			return err
		}
		if !resp.GetFound() {
			return fmt.Errorf("no value is set for %s; set one with `ocel env set %s=<VALUE>`%s", describeCell(key, opts), key, wording.DescriptionLine(descriptions(definitions)[key]))
		}

		if opts.reveal {
			if err := consentToReveal(definitions, key, opts, run.Phase(progressv1.Phase_PHASE_CHECK).Warn); err != nil {
				return err
			}
		}
		if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
			return writeEnvGetJSON(stdout, req, resp)
		}
		m := resp.GetMetadata()
		if opts.reveal {
			fmt.Fprintln(stdout, resp.GetValue())
			return nil
		}
		if target := m.GetTarget(); target != nil {
			fmt.Fprintf(stdout, "%s references %s — version %d, pointed %s\n", describeCell(key, opts), describeCoordinate(target), m.GetVersion(), terminal.EpochDate(m.GetUpdatedAt()))
			fmt.Fprintln(stdout, "Pass --reveal to print the value it reads. Edit that value where it is set.")
			return nil
		}
		fmt.Fprintf(stdout, "%s — version %d, %d bytes, updated %s\n", describeCell(key, opts), m.GetVersion(), m.GetSize(), terminal.EpochDate(m.GetUpdatedAt()))
		if publisher := m.GetPublisher(); publisher != "" {
			fmt.Fprintf(stdout, "Published by %s, the program to change it in.\n", publisher)
		}
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
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env rm", []cloudv1.Scope{cloudv1.Scope_SCOPE_VARIABLES_WRITE}, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, status *contractv1.PreflightResponse) error {
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		values := envValues(provider, cfg, opts)
		at := envCoordinate(key, opts)
		var deleted bool
		err = runInSpan(run, progressv1.Phase_PHASE_DESTROY, cfg, progress.Removing.Title(describeValues([]string{key}, opts)), func() error {
			seen, err := values.Version(ctx, at)
			if err != nil {
				return err
			}
			deleted, err = values.Delete(ctx, at, &seen)
			return staleCell(err, key, opts)
		})
		if err != nil {
			return err
		}
		if !deleted {
			fmt.Fprintf(stdout, "No value was set for %s.\n", describeCell(key, opts))
			return nil
		}
		fmt.Fprintf(stdout, "Removed %s.\n", describeCell(key, opts))
		definitions, groups, err := declaredVariables(ctx, dependencies, cfg, provider, key, opts, run)
		if err != nil {
			return err
		}
		return printGroupProgress(ctx, run, variableStore, cfg, definitions, groups, opts, []envSetPair{{key: key}}, stdout)
	})
}

func writeEnvGetJSON(stdout io.Writer, req *variablestorev1.GetValueRequest, resp *variablestorev1.GetValueResponse) error {
	m := resp.GetMetadata()
	result := &resultv1.EnvGetResult{
		Tier:       req.GetTier(),
		Coordinate: newResultCoordinate(req.GetCoordinate()),
		Version:    m.GetVersion(),
		Size:       m.GetSize(),
		UpdatedAt:  terminal.EpochRFC3339(m.GetUpdatedAt()),
		Target:     newResultCoordinate(m.GetTarget()),
		Revealed:   req.GetReveal(),
		Publisher:  m.GetPublisher(),
	}
	if req.GetReveal() {
		value := resp.GetValue()
		result.Value = &value
	}
	return terminal.WriteResultJSON(stdout, result)
}

func runEnvHistory(ctx context.Context, dependencies Dependencies, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env history", nil, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		req := &variablestorev1.ListVersionsRequest{
			Tier:       opts.tier(),
			Coordinate: wireCoordinate(cfg.Slug, key, opts),
		}
		var resp *variablestorev1.ListVersionsResponse
		err = runInSpan(run, progressv1.Phase_PHASE_CHECK, cfg, progress.Reading.Title("the history of "+describeValues([]string{key}, opts)), func() (err error) {
			resp, err = variableStore.ListVersions(ctx, req)
			return err
		})
		if err != nil {
			return err
		}
		if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
			return writeEnvHistoryJSON(stdout, req, resp.GetVersions())
		}
		renderVersions(stdout, describeCell(key, opts), resp.GetVersions())
		return nil
	})
}

func writeEnvHistoryJSON(stdout io.Writer, req *variablestorev1.ListVersionsRequest, versions []*variablestorev1.VersionEntry) error {
	result := &resultv1.EnvHistoryResult{
		Tier:       req.GetTier(),
		Coordinate: newResultCoordinate(req.GetCoordinate()),
		Versions:   make([]*resultv1.EnvVersion, 0, len(versions)),
	}
	for _, v := range versions {
		result.Versions = append(result.Versions, &resultv1.EnvVersion{Version: v.GetVersion(), CreatedAt: terminal.EpochRFC3339(v.GetCreatedAt()), Size: v.GetSize()})
	}
	return terminal.WriteResultJSON(stdout, result)
}

func renderVersions(stdout io.Writer, cell string, versions []*variablestorev1.VersionEntry) {
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
