package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provision"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func storeValues(projectEnv, dotfile map[string]string) map[string]string {
	values := make(map[string]string, len(projectEnv)+len(dotfile))
	for k, v := range projectEnv {
		values[k] = v
	}
	for k, v := range dotfile {
		values[k] = v
	}
	return values
}

func resolveProjectConfig(ctx context.Context, d session.Session, apiURL, token, projectID string, stderr io.Writer) provision.ProjectConfig {
	cfg, err := d.FetchProjectConfig(ctx, apiURL, token, projectID)
	if err == nil {
		return cfg
	}
	fmt.Fprintf(stderr, "could not reach the control plane (%v). This run resolves values from %s alone; anything set with `ocel env set` is not in play.\n", err, dotenv.FileName)
	return provision.ProjectConfig{ProjectID: projectID, APIURL: apiURL, Token: token}
}

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

func checkStatableBinding(apps []projectconfig.App, stated, configName string, scoped map[string][]string) error {
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
		scopedPlural(keys), strings.Join(bindings, "\n  "), folderLabel(stated), dotenv.FileName, configName)
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

func reportUnreadableLines(stdout io.Writer, unreadable []int) {
	if len(unreadable) == 0 {
		return
	}
	numbers := make([]string, 0, len(unreadable))
	for _, line := range unreadable {
		numbers = append(numbers, strconv.Itoa(line))
	}
	if len(unreadable) == 1 {
		fmt.Fprintf(stdout, "%s line %s is not KEY=VALUE and was ignored.\n", dotenv.FileName, numbers[0])
		return
	}
	fmt.Fprintf(stdout, "%s lines %s are not KEY=VALUE and were ignored.\n", dotenv.FileName, strings.Join(numbers, ", "))
}

var (
	dotfileWatchedAdvice  = fmt.Sprintf("editing %s re-resolves this run; saving it is enough.", dotenv.FileName)
	dotfileReadOnceAdvice = fmt.Sprintf("%s is read once, at startup; editing it takes effect on the next `ocel run`.", dotenv.FileName)
)

func reportDotfile(stdout io.Writer, dir string, values map[string]string, advice string) {
	if len(values) == 0 {
		return
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	fmt.Fprintf(stdout, "resolved %s from %s. That file is yours alone — a teammate's checkout has its own, so nothing set here reaches anyone else and a deploy resolves none of it.\n",
		strings.Join(keys, ", "), dotenv.FileName)
	fmt.Fprintf(stdout, "dev delivers every value to the app in plaintext under its own name; a deploy keeps a sensitive value out of the function environment and a live one out of the artifact.\n")
	fmt.Fprintln(stdout, advice)
	if !gitIgnoresDotfile(dir) {
		fmt.Fprintf(stdout, "%s is not matched by this project's .gitignore. Add it before committing — it holds values nothing else may see.\n", dotenv.FileName)
	}
}

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
