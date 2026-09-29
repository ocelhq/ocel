package variables

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/pkg/envsource"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const EnvSourceGroup = "env source"

type EnvSource struct {
	ID          string
	CanCreate   bool
	CanUpdate   bool
	URLs        map[string]string
	Present     []Cell
	Credentials []string
}

func (s EnvSource) OwnsValues() bool {
	return s.ID != "" && s.ID != string(envsource.Builtin)
}

func (s EnvSource) CanSet(at Coordinate) bool {
	return at.Environment == "" && s.OwnsValues() && (s.CanCreate || s.CanUpdate) && !s.isCredential(at.Cell.Key)
}

func (s EnvSource) credentialDefinitions(site string) []*resourcesv1.VariableDefinition {
	out := make([]*resourcesv1.VariableDefinition, 0, len(s.Credentials))
	for _, key := range s.Credentials {
		out = append(out, &resourcesv1.VariableDefinition{
			Key:         key,
			Class:       resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
			Required:    true,
			Group:       EnvSourceGroup,
			Source:      site,
			Description: "Read by ocel alone to log in to " + s.ID + "; never handed to an app.",
		})
	}
	return out
}

func (s EnvSource) credentialGroups() []*resourcesv1.GroupDefinition {
	if len(s.Credentials) == 0 {
		return nil
	}
	return []*resourcesv1.GroupDefinition{{
		Key:         EnvSourceGroup,
		Required:    true,
		Description: "How ocel logs in to " + s.ID + ".",
	}}
}

func (s EnvSource) isCredential(key string) bool {
	return slices.Contains(s.Credentials, key)
}

func (s EnvSource) areCredentials(problems []*resourcesv1.VariableProblem) bool {
	return len(problems) > 0 && !slices.ContainsFunc(problems, func(problem *resourcesv1.VariableProblem) bool {
		return problem.GetFolder() != "" || !s.isCredential(problem.GetKey())
	})
}

func (s EnvSource) remedy(problems []*resourcesv1.VariableProblem, scope Scope) string {
	var folders []string
	for _, problem := range problems {
		if !slices.Contains(folders, problem.GetFolder()) {
			folders = append(folders, problem.GetFolder())
		}
	}
	slices.Sort(folders)
	var where []string
	for _, folder := range folders {
		if url := s.URLs[folder]; url != "" {
			where = append(where, runui.VariableFolderName(folder)+" "+url)
		}
	}
	out := remedyVerb(problems) + " in " + s.ID
	if len(where) > 0 {
		out += " (" + strings.Join(where, ", ") + ")"
	}
	out += ", then deploy again"
	if scope.isPreview() && scope.Environment != "" {
		key := "<KEY>"
		if len(problems) == 1 {
			key = problems[0].GetKey()
		}
		out += fmt.Sprintf("; or set a value for %s alone with `ocel env set %s=<VALUE> --preview --environment %s`", scope.Environment, key, scope.Environment)
	}
	return out
}

func remedyVerb(problems []*resourcesv1.VariableProblem) string {
	var unset, invalid bool
	for _, problem := range problems {
		if problem.GetKind() == resourcesv1.VariableProblem_KIND_INVALID {
			invalid = true
		} else {
			unset = true
		}
	}
	verb := "set"
	switch {
	case unset && invalid:
		verb = "set or fix"
	case invalid:
		verb = "fix"
	}
	if len(problems) == 1 {
		return verb + " it"
	}
	return verb + " them"
}

func ListUndeclared(declared []*resourcesv1.VariableDefinition, source EnvSource) []string {
	if !source.OwnsValues() {
		return nil
	}
	present := slices.Clone(source.Present)
	slices.SortFunc(present, func(a, b Cell) int { return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Folder, b.Folder)) })
	var out []string
	for _, cell := range present {
		if declares(declared, cell) {
			continue
		}
		out = append(out, fmt.Sprintf("%s has %s in %s, and nothing this project declares reads it there: declare it, or remove it from %s",
			source.ID, cell.Key, runui.VariableFolderName(cell.Folder), source.ID))
	}
	return out
}

func declares(definitions []*resourcesv1.VariableDefinition, cell Cell) bool {
	return slices.ContainsFunc(definitions, func(definition *resourcesv1.VariableDefinition) bool {
		return definition.GetKey() == cell.Key && (len(definition.GetFolders()) == 0 || slices.Contains(definition.GetFolders(), cell.Folder))
	})
}
