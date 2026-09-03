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
	Problems []*resourcesv1.VariableProblem
	Scope    Scope
}

func (r *Refusal) Error() string {
	owed := r.Owed()
	lines := append(Lines(owed.GetCells(), Plain), "", RemedyLine(owed.GetRemedy()))
	return strings.Join(lines, "\n")
}

func (r *Refusal) Owed() *streamv1.VariablesOwed {
	cells := make([]*streamv1.OwedVariable, 0, len(r.Problems))
	for _, problem := range r.Problems {
		cells = append(cells, &streamv1.OwedVariable{
			Key:    problem.GetKey(),
			Folder: problem.GetFolder(),
			Reason: reason(problem),
		})
	}
	return &streamv1.VariablesOwed{Cells: cells, Remedy: r.remedy()}
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
	cmd := fmt.Sprintf("ocel env set %s <VALUE>", key)
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
	out := []string{paint.Fail(Mark) + " " + Headline(len(cells)), ""}
	keyWidth, folderWidth := 0, 0
	for _, cell := range cells {
		keyWidth = max(keyWidth, len(cell.GetKey()))
		folderWidth = max(folderWidth, len(folderName(cell.GetFolder())))
	}
	for _, cell := range cells {
		key := fmt.Sprintf("%-*s", keyWidth, cell.GetKey())
		folder := fmt.Sprintf("%-*s", folderWidth, folderName(cell.GetFolder()))
		out = append(out, indent+paint.Fail(Mark)+" "+key+indent+paint.Faint(folder)+indent+cell.GetReason())
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
