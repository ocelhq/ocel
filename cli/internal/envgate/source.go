package envgate

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const builtinSource = "builtin"

type Source struct {
	ID       string
	Writable bool
	Links    map[string]string
	Present  []Cell
}

func (g *Gate) Source() Source { return g.scope.Source }

func (r *Refusal) Description(key string) string { return r.definition(key).GetDescription() }

func (s Source) Owns() bool { return s.ID != "" && s.ID != builtinSource }

func (s Source) Link(folder string) string { return s.Links[folder] }

func (s Source) remedy(problems []*resourcesv1.VariableProblem) string {
	var folders []string
	for _, problem := range problems {
		if !slices.Contains(folders, problem.GetFolder()) {
			folders = append(folders, problem.GetFolder())
		}
	}
	slices.Sort(folders)
	var where []string
	for _, folder := range folders {
		if link := s.Link(folder); link != "" {
			where = append(where, folderName(folder)+" "+link)
		}
	}
	out := remedyVerb(problems) + " in " + s.ID
	if len(where) > 0 {
		out += " (" + strings.Join(where, ", ") + ")"
	}
	return out + ", then deploy again"
}

func remedyVerb(problems []*resourcesv1.VariableProblem) string {
	var missing, invalid bool
	for _, problem := range problems {
		if problem.GetKind() == resourcesv1.VariableProblem_KIND_INVALID {
			invalid = true
		} else {
			missing = true
		}
	}
	verb := "set"
	switch {
	case missing && invalid:
		verb = "set or fix"
	case invalid:
		verb = "fix"
	}
	if len(problems) == 1 {
		return verb + " it"
	}
	return verb + " them"
}

func Drift(definitions []*resourcesv1.VariableDefinition, source Source) []string {
	if !source.Owns() {
		return nil
	}
	present := slices.Clone(source.Present)
	slices.SortFunc(present, func(a, b Cell) int { return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Folder, b.Folder)) })
	var out []string
	for _, cell := range present {
		if declares(definitions, cell) {
			continue
		}
		out = append(out, fmt.Sprintf("%s holds %s in %s, and nothing this project declares reads it there: declare it, or remove it from %s", source.ID, cell.Key, folderName(cell.Folder), source.ID))
	}
	return out
}

func declares(definitions []*resourcesv1.VariableDefinition, cell Cell) bool {
	return slices.ContainsFunc(definitions, func(definition *resourcesv1.VariableDefinition) bool {
		return definition.GetKey() == cell.Key && (len(definition.GetFolders()) == 0 || slices.Contains(definition.GetFolders(), cell.Folder))
	})
}
