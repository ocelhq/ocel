package dev

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type invocation struct {
	name   string
	source valueSource
}

func (i invocation) command() string {
	return "ocel " + i.name
}

func describeRefusal(err error, dotfileKeys map[string]struct{}, run invocation) error {
	var refusal *variables.MissingError
	if !errors.As(err, &refusal) {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s not ready — the app has not been started.\n", variablesAre(len(refusal.Problems)))
	for _, problem := range refusal.Problems {
		cell := variables.Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}
		fmt.Fprintf(&b, "\n  %s%s\n    %s\n    fix: %s\n",
			cellLabel(cell), readBy(refusal.Scope.Apps, cell.Folder), whyUnready(problem), run.source.remedy(cell.Key))
		if hint := shellHint(cell.Key, dotfileKeys, run); hint != "" {
			b.WriteString("    " + hint + "\n")
		}
	}
	fmt.Fprintf(&b, "\nSet the values above in %s, then run `%s` again.", run.source.where(), run.command())
	described := errors.New(b.String())
	var raised *clierror.Error
	if !errors.As(err, &raised) {
		return described
	}
	coded := *raised
	coded.Hint = fmt.Sprintf("set %s in %s, then run `%s` again", english.And(missingKeys(refusal.Problems)), run.source.where(), run.command())
	coded.Cause = described
	return &coded
}

func missingKeys(problems []*resourcesv1.VariableProblem) []string {
	var keys []string
	for _, problem := range problems {
		if !slices.Contains(keys, problem.GetKey()) {
			keys = append(keys, problem.GetKey())
		}
	}
	return keys
}

func shellHint(key string, dotfileKeys map[string]struct{}, run invocation) string {
	if _, inFile := dotfileKeys[key]; inFile {
		return ""
	}
	if _, inShell := os.LookupEnv(key); !inShell {
		return ""
	}
	return fmt.Sprintf("%s is set in this shell, but `%s` resolves values from %s so every developer's run is the same.", key, run.command(), run.source.where())
}

func variablesAre(n int) string {
	if n == 1 {
		return "1 variable is"
	}
	return fmt.Sprintf("%d variables are", n)
}

func whyUnready(problem *resourcesv1.VariableProblem) string {
	if problem.GetKind() != resourcesv1.VariableProblem_KIND_INVALID {
		return "no value is set"
	}
	if detail := problem.GetDetail(); detail != "" {
		return "set, but it does not satisfy its schema: " + detail
	}
	return "set, but it does not satisfy its schema"
}

func cellLabel(cell variables.Cell) string {
	if cell.Folder == "" {
		return cell.Key + " (project root)"
	}
	return cell.Key + " (" + cell.Folder + ")"
}

func readBy(apps []variables.App, folder string) string {
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

func refuseUnstatableBinding(source valueSource, apps []project.App, stated, configName string, scoped map[string][]string) error {
	var keys []string
	losing := make([]bool, len(apps))
	for key, folders := range scoped {
		if slices.Contains(folders, stated) {
			continue
		}
		lost := false
		for i, app := range apps {
			if slices.Contains(folders, app.Folder) {
				losing[i] = true
				lost = true
			}
		}
		if lost {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)

	var bindings []string
	for i, app := range apps {
		if losing[i] {
			bindings = append(bindings, app.Name+" binds "+folderLabel(app.Folder))
		}
	}

	return fmt.Errorf(`%s scoped to a folder this run cannot state — the app has not been started.

  %s

`+"`ocel dev` and `ocel run` spawn one child for the whole project and nothing tells it which app that child is, so the binding they state is %s. A scoped read refuses under it, even with the value in %s.\n\nfix: bind every app to the same folder in %s, or drop `folders:` from those declarations",
		scopedPlural(keys), strings.Join(bindings, "\n  "), folderLabel(stated), source.where(), configName)
}

func scopedPlural(keys []string) string {
	if len(keys) == 1 {
		return keys[0] + " is"
	}
	return strings.Join(keys, ", ") + " are"
}

func folderLabel(folder string) string {
	if folder == "" {
		return "the project root"
	}
	return folder
}
