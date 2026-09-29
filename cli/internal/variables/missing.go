package variables

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/terminal"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

type MissingError struct {
	Problems    []*resourcesv1.VariableProblem
	Definitions []*resourcesv1.VariableDefinition
	Groups      []*resourcesv1.GroupDefinition
	Scope       Scope
}

func (r *MissingError) Error() string {
	missing := r.Variables()
	lines := append(terminal.MissingVariablesLines(missing, terminal.Presentation{}), "", terminal.MissingVariablesRemedy(missing.GetRemedy()))
	return strings.Join(lines, "\n")
}

func (r *MissingError) Detail() string { return "" }

func (r *MissingError) Variables() *streamv1.MissingVariables {
	cells := make([]*streamv1.MissingVariable, 0, len(r.Problems))
	groups := make([]*streamv1.MissingGroup, 0, len(r.Groups))
	listed := map[string]bool{}
	for _, problem := range r.Problems {
		definition := r.definition(problem.GetKey())
		group := definition.GetGroup()
		cells = append(cells, &streamv1.MissingVariable{
			Key:         problem.GetKey(),
			Folder:      problem.GetFolder(),
			Reason:      reason(problem),
			Description: definition.GetDescription(),
			Group:       group,
		})
		if group == "" || listed[group] {
			continue
		}
		listed[group] = true
		groups = append(groups, &streamv1.MissingGroup{Key: group, Description: groupDescription(r.Groups, group)})
	}
	return &streamv1.MissingVariables{Cells: cells, Remedy: r.remedy(), Groups: groups}
}

func (r *MissingError) Description(key string) string {
	return r.definition(key).GetDescription()
}

func (r *MissingError) definition(key string) *resourcesv1.VariableDefinition {
	for _, definition := range r.Definitions {
		if definition.GetKey() == key {
			return definition
		}
	}
	return nil
}

func (r *MissingError) remedy() string {
	if r.Scope.Browser {
		return withPreview("ocel env ui", r.Scope)
	}
	key, folder := "<KEY>", "<FOLDER>"
	if len(r.Problems) == 1 {
		key, folder = r.Problems[0].GetKey(), r.Problems[0].GetFolder()
	}
	if r.Scope.EnvSource.areCredentials(r.Problems) {
		return withPreview(fmt.Sprintf("ocel env set %s=<VALUE>", key), r.Scope)
	}
	if r.Scope.EnvSource.OwnsValues() {
		return r.Scope.EnvSource.remedy(r.Problems, r.Scope)
	}
	inFolder := false
	for _, problem := range r.Problems {
		inFolder = inFolder || problem.GetFolder() != ""
	}
	cmd := fmt.Sprintf("ocel env set %s=<VALUE>", key)
	if inFolder {
		cmd += " --folder " + folder
	}
	return withEnvironment(cmd, r.Scope)
}

func withPreview(cmd string, scope Scope) string {
	if scope.isPreview() {
		return cmd + " --preview"
	}
	return cmd
}

func withEnvironment(cmd string, scope Scope) string {
	cmd = withPreview(cmd, scope)
	if scope.isPreview() && scope.Environment != "" {
		return cmd + " --environment " + scope.Environment
	}
	return cmd
}

func reason(problem *resourcesv1.VariableProblem) string {
	if problem.GetKind() != resourcesv1.VariableProblem_KIND_INVALID {
		return "no value"
	}
	if detail := problem.GetDetail(); detail != "" {
		return "set, but " + detail
	}
	return "set, but it does not satisfy its schema"
}
