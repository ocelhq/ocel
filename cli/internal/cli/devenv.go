package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/localsource"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
)

type devSource struct {
	id     string
	values map[string]string
}

func readDevSource(ctx context.Context, cfg *projectconfig.Config) (devSource, error) {
	source, err := localsource.Dev(cfg.EnvSource.Dev, cfg.Dir, os.LookupEnv)
	if err != nil {
		return devSource{}, err
	}
	if source == nil {
		return devSource{id: string(envsource.Dotenv)}, nil
	}
	folders := []string{""}
	if folder := appbuilder.AppFolder(cfg.Apps); folder != "" {
		folders = append(folders, folder)
	}
	resolved, err := source.Resolve(ctx, folders)
	if err != nil {
		return devSource{}, fmt.Errorf("read envSource.dev (%s): %w", source.ID(), err)
	}
	values := map[string]string{}
	for _, folder := range folders {
		for cell, held := range resolved {
			if cell.Folder == folder {
				values[cell.Key] = string(held.Value)
			}
		}
	}
	return devSource{id: source.ID(), values: values}, nil
}

func (s devSource) dotenv() bool { return s.id == string(envsource.Dotenv) }

func (s devSource) files() []string {
	if s.dotenv() {
		return []string{dotenv.FileName, dotenv.LocalFileName}
	}
	return []string{dotenv.LocalFileName}
}

func (s devSource) where() string {
	if s.dotenv() {
		return dotenv.FileName
	}
	return s.id + " and " + dotenv.LocalFileName
}

type devLayer struct {
	from       string
	file       bool
	values     map[string]string
	unreadable []int
}

type devValues []devLayer

func (s devSource) read(dir string) (devValues, error) {
	var layers devValues
	if !s.dotenv() {
		layers = append(layers, devLayer{from: s.id, values: s.values})
	}
	for _, name := range s.files() {
		file, err := dotenv.LoadNamed(dir, name)
		if err != nil {
			return nil, err
		}
		layers = append(layers, devLayer{from: name, file: true, values: file.Values, unreadable: file.Unreadable})
	}
	return layers, nil
}

func (v devValues) merged() map[string]string {
	merged := map[string]string{}
	for _, layer := range v {
		for key, value := range layer.values {
			merged[key] = value
		}
	}
	return merged
}

func (v devValues) keys() map[string]struct{} {
	keys := map[string]struct{}{}
	for _, layer := range v {
		for key := range layer.values {
			keys[key] = struct{}{}
		}
	}
	return keys
}

type invocation struct {
	name   string
	source devSource
}

func (i invocation) command() string {
	return "ocel " + i.name
}

func devRefusal(err error, heldKeys map[string]struct{}, run invocation) error {
	var refusal *envgate.Refusal
	if !errors.As(err, &refusal) {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s not ready — the app has not been started.\n", devPlural(len(refusal.Problems)))
	for _, problem := range refusal.Problems {
		cell := envgate.Cell{Key: problem.GetKey(), Folder: problem.GetFolder()}
		fmt.Fprintf(&b, "\n  %s%s\n    %s\n    fix: %s\n",
			devCellLabel(cell), devReadBy(refusal.Scope.Apps, cell.Folder), whyUnready(problem), run.source.fix(cell.Key))
		if hint := shellHint(cell.Key, heldKeys, run); hint != "" {
			b.WriteString("    " + hint + "\n")
		}
	}
	fmt.Fprintf(&b, "\nSet the values above in %s, then run `%s` again.", run.source.where(), run.command())
	return errors.New(b.String())
}

func (s devSource) fix(key string) string {
	if s.dotenv() {
		return fmt.Sprintf("add %s=<VALUE> to %s", key, dotenv.FileName)
	}
	return fmt.Sprintf("set %s in %s, or add %s=<VALUE> to %s", key, s.id, key, dotenv.LocalFileName)
}

func shellHint(key string, heldKeys map[string]struct{}, run invocation) string {
	if _, held := heldKeys[key]; held {
		return ""
	}
	if _, inShell := os.LookupEnv(key); !inShell {
		return ""
	}
	return fmt.Sprintf("%s is set in this shell, but `%s` resolves values from %s so every developer's run is the same.", key, run.command(), run.source.where())
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

func reportUnreadableLines(stdout io.Writer, values devValues) {
	for _, layer := range values {
		if len(layer.unreadable) == 0 {
			continue
		}
		numbers := make([]string, 0, len(layer.unreadable))
		for _, line := range layer.unreadable {
			numbers = append(numbers, strconv.Itoa(line))
		}
		if len(layer.unreadable) == 1 {
			fmt.Fprintf(stdout, "%s line %s is not KEY=VALUE and was ignored.\n", layer.from, numbers[0])
			continue
		}
		fmt.Fprintf(stdout, "%s lines %s are not KEY=VALUE and were ignored.\n", layer.from, strings.Join(numbers, ", "))
	}
}

func (v devValues) advice(watched bool) string {
	var files []string
	for _, layer := range v {
		if layer.file {
			files = append(files, layer.from)
		}
	}
	if watched {
		return fmt.Sprintf("editing %s re-resolves this run; saving it is enough.", strings.Join(files, " or "))
	}
	return fmt.Sprintf("%s is read once, at startup; editing it takes effect on the next `ocel run`.", strings.Join(files, " or "))
}

func reportDevValues(stdout io.Writer, dir string, values devValues, watched bool) {
	reported := false
	for _, layer := range values {
		if len(layer.values) == 0 {
			continue
		}
		reported = true
		keys := strings.Join(slices.Sorted(maps.Keys(layer.values)), ", ")
		if !layer.file {
			fmt.Fprintf(stdout, "resolved %s from %s, the dev env source, read once as this run started.\n", keys, layer.from)
			continue
		}
		fmt.Fprintf(stdout, "resolved %s from %s. That file is yours alone — a teammate's checkout has its own, so nothing set here reaches anyone else and a deploy resolves none of it.\n",
			keys, layer.from)
	}
	if !reported {
		return
	}
	fmt.Fprintf(stdout, "dev delivers every value to the app in plaintext under its own name; a deploy keeps a sensitive value out of the function environment and a live one out of the artifact.\n")
	fmt.Fprintln(stdout, values.advice(watched))
	for _, layer := range values {
		if layer.file && len(layer.values) > 0 && !gitIgnores(dir, layer.from) {
			fmt.Fprintf(stdout, "%s is not matched by this project's .gitignore. Add it before committing — it holds values nothing else may see.\n", layer.from)
		}
	}
}

func gitIgnores(dir, name string) bool {
	file, err := os.Open(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return false
	}
	defer file.Close()

	ignored := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		pattern := strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(scanner.Text()), "/"), "/")
		reincluded := strings.HasPrefix(pattern, "!")
		if matched, _ := path.Match(strings.TrimPrefix(pattern, "!"), name); matched {
			ignored = !reincluded
		}
	}
	return ignored
}
