package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// devRefusal restates a gate refusal in the terms a dev run can act on. The
// verdict is the deploy's verdict — same gate, same two-hop rule, same schema
// report from the declaring process — but the remedy cannot be: `ocel env set`
// needs a cloud provider and a bootstrapped store, and dev's dotfile exists
// precisely so getting started needs neither. A refusal whose fix cannot be run
// is worse than no fix at all, so dev names the file instead.
//
// It is given the file's key names and not its values, so the refusal has no
// value to print however it is later edited.
func devRefusal(err error, dotfileKeys map[string]struct{}) error {
	var refusal *envgate.Refusal
	if !errors.As(err, &refusal) {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s not ready — the app has not been started.\n", devPlural(len(refusal.Problems)))
	for _, problem := range refusal.Problems {
		cell := envgate.Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}
		fmt.Fprintf(&b, "\n  %s%s\n    %s\n    fix: add %s=<VALUE> to %s\n",
			devCellLabel(cell), devReadBy(refusal.Scope.Apps, cell.Folder), whyUnready(problem), cell.Key, dotenv.FileName)
		if hint := shellHint(cell.Key, dotfileKeys); hint != "" {
			b.WriteString("    " + hint + "\n")
		}
	}
	fmt.Fprintf(&b, "\nSet the values above in %s, then run `ocel dev` again.", dotenv.FileName)
	return errors.New(b.String())
}

// shellHint covers the one refusal a developer cannot otherwise explain: the
// name is right there in their own environment. Dev resolves from the file
// rather than the shell on purpose — a verdict that depended on unversioned
// machine state would differ per developer, which is the failure variables
// exist to prevent — so the refusal says which place it looked rather than
// leaving the developer to guess. The value itself is never read out.
func shellHint(key string, dotfileKeys map[string]struct{}) string {
	if _, inFile := dotfileKeys[key]; inFile {
		return ""
	}
	if _, inShell := os.LookupEnv(key); !inShell {
		return ""
	}
	return fmt.Sprintf("%s is set in this shell, but `ocel dev` resolves values from %s so every developer's run is the same.", key, dotenv.FileName)
}

func dotfileKeySet(values map[string]string) map[string]struct{} {
	keys := make(map[string]struct{}, len(values))
	for key := range values {
		keys[key] = struct{}{}
	}
	return keys
}

func devPlural(n int) string {
	if n == 1 {
		return "1 variable is"
	}
	return fmt.Sprintf("%d variables are", n)
}

func whyUnready(problem *resourcesv1.VariableProblem) string {
	if problem.GetKind() == resourcesv1.VariableProblem_KIND_INVALID {
		return "set, but it does not satisfy its schema: " + problem.GetDetail()
	}
	return "no value is set"
}

func devCellLabel(cell envgate.Cell) string {
	if cell.Folder == "" {
		return cell.Key + " (project root)"
	}
	return cell.Key + " (" + cell.Folder + ")"
}

// readBy names the apps a failing cell belongs to. A root cell is the fallback
// for every app; a folder cell is read only by the app bound there.
func devReadBy(apps []envgate.App, folder string) string {
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

// checkStatableBinding refuses a run that would cost an app a read it has. Dev
// spawns one child for the whole project and nothing tells it which app that
// child is, so it states the folder every app agrees on; where two apps bind
// different ones it can only state the project root, and a read scoped
// elsewhere then refuses at runtime with its value sitting in that very
// environment. Letting the gate pass and the SDK throw would be precisely the
// crash at the first read the gate exists to replace, so dev says here what it
// cannot do.
//
// It refuses only for a key some app's own binding is in the scope of, and that
// the stated binding is not — exactly the reads this run loses. A key scoped
// where no app is bound is unreadable under every binding, including the ones a
// deploy states, so dev is as silent about it as a deploy is rather than
// refusing a project that deploys.
func checkStatableBinding(apps []projectconfig.App, stated string, scoped map[string][]string) error {
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
	sort.Strings(keys)

	var bindings []string
	for i, app := range apps {
		if losing[i] {
			bindings = append(bindings, app.Name+" binds "+folderLabel(app.Folder))
		}
	}

	return fmt.Errorf(`%s scoped to a folder this run cannot state — the app has not been started.

  %s

`+"`ocel dev` and `ocel run` spawn one child for the whole project and nothing tells it which app that child is, so the binding they state is %s. A scoped read refuses under it, even with the value in %s.\n\nfix: bind every app to the same folder in ocel.config.ts, or drop `folders:` from those declarations.",
		scopedPlural(keys), strings.Join(bindings, "\n  "), folderLabel(stated), dotenv.FileName)
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

// reportDotfile states what resolving from a file costs, at the moment it is
// done, because none of it is visible from the code that reads the values.
// Collaboration disappears — a shared store is one project's answer, a file is
// one machine's — and so does confidentiality, because dev's only delivery
// mechanism is the environment, so every class lands in the child in plaintext
// under its own name where a deploy would keep a sensitive value out of the
// function environment and a live one out of the artifact.
//
// It runs where the file is read rather than where the values are resolved, so
// a run the gate goes on to refuse still says where it looked, and a watcher's
// re-resolve does not reprint the whole notice on every save.
//
// It is told from key names and line numbers, never values: this is the one
// file whose contents nothing else may see, so the notice about it cannot be
// what prints them.
func reportDotfile(stdout io.Writer, dir string, values map[string]string, unreadable []int) {
	if len(unreadable) > 0 {
		numbers := make([]string, 0, len(unreadable))
		for _, line := range unreadable {
			numbers = append(numbers, strconv.Itoa(line))
		}
		if len(unreadable) == 1 {
			fmt.Fprintf(stdout, "%s line %s is not KEY=VALUE and was ignored.\n", dotenv.FileName, numbers[0])
		} else {
			fmt.Fprintf(stdout, "%s lines %s are not KEY=VALUE and were ignored.\n", dotenv.FileName, strings.Join(numbers, ", "))
		}
	}
	if len(values) == 0 {
		return
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	fmt.Fprintf(stdout, "resolved %s from %s. That file is yours alone — a teammate's checkout has its own, so nothing set here reaches anyone else and a deploy resolves none of it.\n",
		strings.Join(keys, ", "), dotenv.FileName)
	fmt.Fprintf(stdout, "dev delivers every value to the app in plaintext under its own name; a deploy keeps a sensitive value out of the function environment and a live one out of the artifact.\n")
	fmt.Fprintf(stdout, "%s is read once, at startup; editing it takes effect on the next `ocel dev`.\n", dotenv.FileName)
	if !gitIgnoresDotfile(dir) {
		fmt.Fprintf(stdout, "%s is not matched by this project's .gitignore. Add it before committing — it holds values nothing else may see.\n", dotenv.FileName)
	}
}

// gitIgnoresDotfile reads the project's own .gitignore rather than asking git,
// so the check costs nothing and works in a tree that is not a repository yet —
// which is exactly the new project this file is scaffolded for. It reads the
// common spellings and treats a later re-inclusion as decisive, so the one way
// it can be wrong is a nested or global ignore it never saw, which prints a
// redundant line rather than staying quiet about an exposed file.
func gitIgnoresDotfile(dir string) bool {
	file, err := os.Open(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return false
	}
	defer file.Close()

	ignored := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		pattern := strings.TrimSuffix(strings.TrimSpace(scanner.Text()), "/")
		reincluded := strings.HasPrefix(pattern, "!")
		switch strings.TrimPrefix(pattern, "!") {
		case dotenv.FileName, ".env*", ".env.*", "*.env":
			ignored = !reincluded
		}
	}
	return ignored
}
