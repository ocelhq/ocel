package env

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/envsource"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func runEnvList(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env ls", stderr, func(ctx context.Context, run *run.Run, prov *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		definitions, groups, err := declaredVariables(ctx, dependencies, cfg, prov, "", opts, run)
		if err != nil {
			return err
		}
		vars, err := prov.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.ListValues(ctx, &envvarsv1.ListValuesRequest{
			Tier: opts.tier(),
			Slug: cfg.Slug,
		})
		if err != nil {
			return err
		}
		var environments []string
		if opts.preview && overridden(resp.GetValues()) {
			if environments, err = valuestore.ListEnvironmentNames(ctx, prov, cfg.Slug); err != nil {
				return err
			}
		}
		renderValues(stdout, resp.GetValues(), environments, definitions, groups)
		return nil
	})
}

func overridden(values []*envvarsv1.ValueMetadata) bool {
	return slices.ContainsFunc(values, func(v *envvarsv1.ValueMetadata) bool {
		return v.GetCoordinate().GetEnvironment() != ""
	})
}

func renderValues(stdout io.Writer, values []*envvarsv1.ValueMetadata, environments []string, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition) {
	if len(values) == 0 {
		fmt.Fprintln(stdout, "No values set. Set one with `ocel env set <KEY>=<VALUE>`.")
		return
	}
	described := descriptions(definitions)
	belongs := membership(definitions)

	orphans := false
	rows := []listingRow{{cells: []string{"KEY", "DESCRIPTION", "FOLDER", "ENVIRONMENT", "VERSION", "BYTES", "UPDATED", "SOURCE"}}}
	for _, v := range values {
		if belongs[v.GetCoordinate().GetKey()] != "" {
			continue
		}
		row, orphaned := valueRow(v, "", described, environments)
		rows = append(rows, row)
		orphans = orphans || orphaned
	}
	for _, group := range groups {
		members := valuesIn(group.GetKey(), values, definitions)
		if len(members) == 0 {
			continue
		}
		rows = append(rows, listingRow{}, listingRow{headline: terminal.VariableGroupHeadline(group.GetKey(), group.GetDescription())})
		for _, v := range members {
			row, orphaned := valueRow(v, listingIndent, described, environments)
			rows = append(rows, row)
			orphans = orphans || orphaned
		}
	}
	writeListing(stdout, rows)

	if orphans {
		fmt.Fprintln(stdout, "\nAn orphaned override belongs to an environment that no longer exists, so nothing will ever read it. Remove one with `ocel env rm <KEY> --preview --environment <ENVIRONMENT>`.")
	}
}

const listingIndent = "  "

type listingRow struct {
	headline string
	lead     string
	cells    []string
}

func valueRow(v *envvarsv1.ValueMetadata, lead string, descriptions map[string]string, environments []string) (listingRow, bool) {
	c := v.GetCoordinate()
	environment := environmentOrAll(c.GetEnvironment())
	orphaned := variables.IsOrphaned(environments, c.GetEnvironment())
	if orphaned {
		environment += " (orphaned)"
	}
	size := fmt.Sprint(v.GetSize())
	source := v.GetEnvSource()
	if source == "" {
		source = string(envsource.Builtin)
	}
	if target := v.GetTarget(); target != nil {
		size, source = "—", describeCoordinate(target)
	}
	return listingRow{lead: lead, cells: []string{
		c.GetKey(), descriptions[c.GetKey()], folderOrRoot(c.GetFolder()), environment,
		fmt.Sprint(v.GetVersion()), size, terminal.EpochDate(v.GetUpdatedAt()), source,
	}}, orphaned
}

func writeListing(stdout io.Writer, rows []listingRow) {
	var widths []int
	for _, row := range rows {
		for i, cell := range row.cells {
			width := utf8.RuneCountInString(cell)
			if i == 0 {
				width += utf8.RuneCountInString(row.lead)
			}
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], width)
		}
	}
	for _, row := range rows {
		if row.cells == nil {
			fmt.Fprintln(stdout, row.headline)
			continue
		}
		line := row.lead
		for i, cell := range row.cells {
			if i == len(row.cells)-1 {
				line += cell
				break
			}
			width := widths[i]
			if i == 0 {
				width -= utf8.RuneCountInString(row.lead)
			}
			line += cell + strings.Repeat(" ", width-utf8.RuneCountInString(cell)+len(listingIndent))
		}
		fmt.Fprintln(stdout, strings.TrimRight(line, " "))
	}
}

func membership(definitions []*resourcesv1.VariableDefinition) map[string]string {
	out := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		out[definition.GetKey()] = definition.GetGroup()
	}
	return out
}

func valuesIn(group string, values []*envvarsv1.ValueMetadata, definitions []*resourcesv1.VariableDefinition) []*envvarsv1.ValueMetadata {
	var out []*envvarsv1.ValueMetadata
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

func environmentOrAll(environment string) string {
	if environment == "" {
		return "—"
	}
	return environment
}
