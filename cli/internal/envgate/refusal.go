package envgate

import (
	"fmt"
	"slices"
	"strings"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

type Refusal struct {
	Problems []*resourcesv1.VariableProblem
	Scope    Scope
}

func (r *Refusal) Error() string {
	return r.Owed() + "\nSet the values above, then run this command again."
}

func (r *Refusal) Owed() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s not ready — nothing has been built.\n", plural(len(r.Problems)))
	for _, problem := range r.Problems {
		cell := Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}
		fmt.Fprintf(&b, "\n  %s\n    %s\n    fix: %s\n",
			describe(cell)+readBy(r.Scope.Apps, cell.Folder), why(problem), fixCommand(cell, r.Scope))
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return "1 variable is"
	}
	return fmt.Sprintf("%d variables are", n)
}

func why(problem *resourcesv1.VariableProblem) string {
	if problem.GetKind() == resourcesv1.VariableProblem_KIND_INVALID {
		return "set, but it does not satisfy its schema: " + problem.GetDetail()
	}
	return "no value is set"
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

func readBy(apps []App, folder string) string {
	var names []string
	for _, app := range apps {
		if folder == "" || app.Folder == folder {
			names = append(names, app.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return ", read by " + strings.Join(names, ", ")
}

func fixCommand(cell Cell, scope Scope) string {
	cmd := fmt.Sprintf("ocel env set %s <VALUE>", cell.Key)
	if cell.Folder != "" {
		cmd += " --folder " + cell.Folder
	}
	if scope.Preview {
		cmd += " --preview"
	}
	return cmd
}
