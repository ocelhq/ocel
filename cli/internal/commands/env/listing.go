package env

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variables/wording"
	"github.com/ocelhq/ocel/pkg/envsource"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func runEnvList(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env ls", nil, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		definitions, groups, err := declaredVariables(ctx, dependencies, cfg, provider, "", opts, run)
		if err != nil {
			return err
		}
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		req := &variablestorev1.ListValuesRequest{
			Tier: opts.tier(),
			Slug: cfg.Slug,
		}
		resp, err := variableStore.ListValues(ctx, req)
		if err != nil {
			return err
		}
		var environments []string
		if opts.preview && overridden(resp.GetValues()) {
			if environments, err = variables.ListEnvironmentNames(ctx, provider, cfg.Slug); err != nil {
				return err
			}
		}
		present := dependencies.Presentation(stdout)
		if present.Format == terminal.FormatJSON {
			return writeEnvListJSON(stdout, req, resp.GetValues(), environments, definitions)
		}
		renderValues(stdout, present, resp.GetValues(), environments, definitions, groups)
		return nil
	})
}

func writeEnvListJSON(stdout io.Writer, req *variablestorev1.ListValuesRequest, values []*variablestorev1.ValueMetadata, environments []string, definitions []*resourcesv1.VariableDefinition) error {
	described := descriptions(definitions)
	belongs := membership(definitions)
	result := &resultv1.EnvListResult{Tier: req.GetTier(), Values: make([]*resultv1.EnvValueSummary, 0, len(values))}
	for _, v := range values {
		c := v.GetCoordinate()
		result.Values = append(result.Values, &resultv1.EnvValueSummary{
			Coordinate:  newResultCoordinate(c),
			Description: described[c.GetKey()],
			Group:       belongs[c.GetKey()],
			Orphaned:    variables.IsOrphaned(environments, c.GetEnvironment()),
			Version:     v.GetVersion(),
			Size:        v.GetSize(),
			UpdatedAt:   terminal.EpochRFC3339(v.GetUpdatedAt()),
			EnvSource:   envSourceOrBuiltin(v),
			Target:      newResultCoordinate(v.GetTarget()),
		})
	}
	return terminal.WriteResultJSON(stdout, result)
}

func overridden(values []*variablestorev1.ValueMetadata) bool {
	return slices.ContainsFunc(values, func(v *variablestorev1.ValueMetadata) bool {
		return v.GetCoordinate().GetEnvironment() != ""
	})
}

func renderValues(stdout io.Writer, present terminal.Presentation, values []*variablestorev1.ValueMetadata, environments []string, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition) {
	if len(values) == 0 {
		fmt.Fprintln(stdout, "No values set. Set one with `ocel env set <KEY>=<VALUE>`.")
		return
	}
	palette := present.Palette()
	described := descriptions(definitions)
	belongs := membership(definitions)

	table := terminal.NewTable(
		terminal.Column{Head: "KEY"},
		terminal.Column{Head: "DESCRIPTION"},
		terminal.Column{Head: "FOLDER"},
		terminal.Column{Head: "ENVIRONMENT"},
		terminal.Column{Head: "VERSION", Align: terminal.AlignRight},
		terminal.Column{Head: "BYTES", Align: terminal.AlignRight},
		terminal.Column{Head: "UPDATED"},
		terminal.Column{Head: "SOURCE"},
	)
	orphans := false
	for _, v := range values {
		if belongs[v.GetCoordinate().GetKey()] != "" {
			continue
		}
		orphans = valueRow(table, present, v, "", described, environments) || orphans
	}
	for _, group := range groups {
		members := valuesIn(group.GetKey(), values, definitions)
		if len(members) == 0 {
			continue
		}
		table.Span("")
		table.Span(palette.Bold(wording.GroupHeadline(group.GetKey(), group.GetDescription())))
		for _, v := range members {
			orphans = valueRow(table, present, v, listingIndent, described, environments) || orphans
		}
	}
	fmt.Fprint(stdout, table.Render(palette, ""))

	if orphans {
		fmt.Fprintf(stdout, "\n%s %s\n", palette.WarnMark(), palette.Commands("An orphaned override's environment is gone, so nothing reads it: `ocel env rm <KEY> --preview --environment <ENVIRONMENT>` removes it."))
	}
}

const listingIndent = "  "

func valueRow(table *terminal.Table, present terminal.Presentation, v *variablestorev1.ValueMetadata, lead string, descriptions map[string]string, environments []string) bool {
	palette := present.Palette()
	c := v.GetCoordinate()
	environment := environmentOrAll(c.GetEnvironment())
	orphaned := variables.IsOrphaned(environments, c.GetEnvironment())
	if orphaned {
		environment = palette.Warning(environment + " (orphaned)")
	}
	size := fmt.Sprint(v.GetSize())
	source := envSourceOrBuiltin(v)
	if target := v.GetTarget(); target != nil {
		size, source = terminal.NoValue, describeCoordinate(target)
	}
	folder := c.GetFolder()
	if folder == "" {
		folder = palette.Muted(folderOrRoot(folder))
	}
	table.Row(lead+palette.Bold(c.GetKey()), descriptions[c.GetKey()], folder, environment,
		fmt.Sprint(v.GetVersion()), size, present.EpochAge(v.GetUpdatedAt()), source)
	return orphaned
}

func membership(definitions []*resourcesv1.VariableDefinition) map[string]string {
	out := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		out[definition.GetKey()] = definition.GetGroup()
	}
	return out
}

func valuesIn(group string, values []*variablestorev1.ValueMetadata, definitions []*resourcesv1.VariableDefinition) []*variablestorev1.ValueMetadata {
	var out []*variablestorev1.ValueMetadata
	for _, definition := range definitions {
		if definition.GetGroup() != group {
			continue
		}
		for _, v := range values {
			if v.GetCoordinate().GetKey() == definition.GetKey() {
				out = append(out, v)
			}
		}
	}
	return out
}

func descriptions(definitions []*resourcesv1.VariableDefinition) map[string]string {
	out := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		out[definition.GetKey()] = definition.GetDescription()
	}
	return out
}

func folderOrRoot(folder string) string {
	if folder == "" {
		return "(project root)"
	}
	return folder
}

func envSourceOrBuiltin(v *variablestorev1.ValueMetadata) string {
	if source := v.GetEnvSource(); source != "" {
		return source
	}
	return envsource.Builtin
}

func environmentOrAll(environment string) string {
	if environment == "" {
		return terminal.NoValue
	}
	return environment
}
