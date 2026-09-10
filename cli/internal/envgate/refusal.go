package envgate

import (
	"fmt"
	"slices"
	"strings"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

const (
	Mark       = "✗"
	rootFolder = "root"
	indent     = "  "
)

type Refusal struct {
	Problems    []*resourcesv1.VariableProblem
	Definitions []*resourcesv1.VariableDefinition
	Groups      []*resourcesv1.GroupDefinition
	Scope       Scope
}

func (r *Refusal) Error() string {
	owed := r.Owed()
	lines := append(render(owed.GetCells(), r.grouping(), Plain), "", RemedyLine(owed.GetRemedy()))
	return strings.Join(lines, "\n")
}

func (r *Refusal) grouping() func(string) (string, string) {
	return func(key string) (string, string) {
		for _, definition := range r.Definitions {
			if definition.GetKey() == key {
				return definition.GetGroup(), groupDescription(r.Groups, definition.GetGroup())
			}
		}
		return "", ""
	}
}

func (r *Refusal) Owed() *streamv1.VariablesOwed {
	cells := make([]*streamv1.OwedVariable, 0, len(r.Problems))
	for _, problem := range r.Problems {
		cells = append(cells, &streamv1.OwedVariable{
			Key:         problem.GetKey(),
			Folder:      problem.GetFolder(),
			Reason:      reason(problem),
			Description: r.description(problem.GetKey()),
		})
	}
	return &streamv1.VariablesOwed{Cells: cells, Remedy: r.remedy()}
}

func (r *Refusal) description(key string) string {
	for _, definition := range r.Definitions {
		if definition.GetKey() == key {
			return definition.GetDescription()
		}
	}
	return ""
}

func (r *Refusal) remedy() string {
	if r.Scope.Browser {
		return withPreview("ocel env ui", r.Scope)
	}
	key, folder := "<KEY>", "<FOLDER>"
	inFolder := false
	for _, problem := range r.Problems {
		inFolder = inFolder || problem.GetFolder() != ""
	}
	if len(r.Problems) == 1 {
		key, folder = r.Problems[0].GetKey(), r.Problems[0].GetFolder()
	}
	cmd := fmt.Sprintf("ocel env set %s=<VALUE>", key)
	if inFolder {
		cmd += " --folder " + folder
	}
	return withPreview(cmd, r.Scope)
}

func withPreview(cmd string, scope Scope) string {
	if scope.Preview {
		return cmd + " --preview"
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

type Paint struct {
	Fail  func(string) string
	Faint func(string) string
}

var Plain = Paint{Fail: plain, Faint: plain}

func plain(s string) string { return s }

func Headline(n int) string {
	if n == 1 {
		return "1 variable is not ready — nothing has been built."
	}
	return fmt.Sprintf("%d variables are not ready — nothing has been built.", n)
}

func Lines(cells []*streamv1.OwedVariable, paint Paint) []string {
	return render(cells, nil, paint)
}

func render(cells []*streamv1.OwedVariable, grouping func(string) (string, string), paint Paint) []string {
	out := []string{paint.Fail(Mark) + " " + Headline(len(cells)), ""}
	keyWidth, folderWidth := 0, 0
	for _, cell := range cells {
		width := len(cell.GetKey())
		if group, _ := groupNameOf(grouping, cell); group != "" {
			width += len(indent)
		}
		keyWidth = max(keyWidth, width)
		folderWidth = max(folderWidth, len(folderName(cell.GetFolder())))
	}

	written := map[string]bool{}
	for _, cell := range cells {
		group, description := groupNameOf(grouping, cell)
		if group == "" {
			out = append(out, owedLines(cell, indent, keyWidth, folderWidth, paint)...)
			continue
		}
		if written[group] {
			continue
		}
		written[group] = true
		out = append(out, indent+paint.Faint(GroupHeadline(group, description)))
		for _, held := range cells {
			if name, _ := groupNameOf(grouping, held); name == group {
				out = append(out, owedLines(held, indent+indent, keyWidth-len(indent), folderWidth, paint)...)
			}
		}
	}
	return out
}

func groupNameOf(grouping func(string) (string, string), cell *streamv1.OwedVariable) (string, string) {
	if grouping == nil {
		return "", ""
	}
	return grouping(cell.GetKey())
}

func owedLines(cell *streamv1.OwedVariable, lead string, keyWidth, folderWidth int, paint Paint) []string {
	key := fmt.Sprintf("%-*s", keyWidth, cell.GetKey())
	folder := fmt.Sprintf("%-*s", folderWidth, folderName(cell.GetFolder()))
	out := []string{lead + paint.Fail(Mark) + " " + key + indent + paint.Faint(folder) + indent + cell.GetReason()}
	if description := cell.GetDescription(); description != "" {
		out = append(out, lead+indent+paint.Faint(description))
	}
	return out
}

func GroupHeadline(group, description string) string {
	out := group + " — set together"
	if description != "" {
		out += " (" + description + ")"
	}
	return out
}

func RemedyLine(remedy string) string {
	return indent + "Fill them in: " + remedy
}

func folderName(folder string) string {
	if folder == "" {
		return rootFolder
	}
	return folder
}

func describeAll(rows []Address) string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, describe(row.Cell))
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func describe(cell Cell) string {
	if cell.Folder == "" {
		return cell.Key + " (project root)"
	}
	return cell.Key + " (" + cell.Folder + ")"
}
