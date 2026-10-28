package env

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

func printGroupProgress(ctx context.Context, run *run.Run, store variablestorev1connect.VariableStoreServiceClient, cfg *project.Project, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, opts envOptions, changed []envSetPair, stdout io.Writer) error {
	touched := map[string]bool{}
	for _, pair := range changed {
		for _, definition := range definitions {
			if definition.GetKey() == pair.key && definition.GetGroup() != "" {
				touched[definition.GetGroup()] = true
			}
		}
	}
	if len(touched) == 0 {
		return nil
	}
	var listed *variablestorev1.ListValuesResponse
	err := runInSpan(ctx, run, progressv1.Phase_PHASE_CHECK, cfg, progress.Reading.Title(describeTierValues(opts)), func(ctx context.Context) (err error) {
		listed, err = store.ListValues(ctx, &variablestorev1.ListValuesRequest{Tier: opts.tier(), Slug: cfg.Slug})
		return err
	})
	if err != nil {
		return err
	}
	present := presentCells(listed.GetValues(), opts.environment)
	for _, completeness := range variables.ReadGroupCompleteness(definitions, groups, present, opts.folder) {
		if !touched[completeness.Key] || len(completeness.Missing) == 0 {
			continue
		}
		fmt.Fprintf(stdout, "%s: %d of %d set. Set together: %s\n",
			completeness.Key, len(completeness.Set), len(completeness.Set)+len(completeness.Missing), strings.Join(completeness.Missing, ", "))
	}
	return nil
}

func presentCells(values []*variablestorev1.ValueMetadata, environment string) []variables.Cell {
	var out []variables.Cell
	for _, value := range values {
		coordinate := value.GetCoordinate()
		if coordinate.GetEnvironment() != "" && coordinate.GetEnvironment() != environment {
			continue
		}
		out = append(out, variables.Cell{Key: coordinate.GetKey(), Folder: coordinate.GetFolder()})
	}
	return out
}
