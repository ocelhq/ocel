package env

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

func printGroupProgress(ctx context.Context, vars envvarsv1connect.EnvVarsServiceClient, slug string, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, opts envOptions, changed []envSetPair, stdout io.Writer) error {
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
	listed, err := vars.ListValues(ctx, &envvarsv1.ListValuesRequest{Tier: opts.tier(), Slug: slug})
	if err != nil {
		return err
	}
	present := presentCells(listed.GetValues(), opts.environment)
	for _, state := range variables.GroupStates(definitions, groups, present, opts.folder) {
		if !touched[state.Key] || len(state.Missing) == 0 {
			continue
		}
		fmt.Fprintf(stdout, "%s: %d of %d set. Set together: %s\n",
			state.Key, len(state.Set), len(state.Set)+len(state.Missing), strings.Join(state.Missing, ", "))
	}
	return nil
}

func presentCells(values []*envvarsv1.ValueMetadata, environment string) []variables.Cell {
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
