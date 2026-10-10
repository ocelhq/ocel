package clientenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

var recordPath = filepath.Join(statedir.Name, "output", "client-digests.json")

type App struct {
	variables.App
	Declared  []*resourcesv1.VariableDefinition
	Variables []variables.Variable
}

func AppsOf(cfg *project.Project, definitions []*resourcesv1.VariableDefinition, values map[string][]variables.Variable) []App {
	scoped := variablescope.Apps(cfg)
	apps := make([]App, 0, len(scoped))
	for _, app := range scoped {
		apps = append(apps, App{App: app, Declared: declaredFor(app, definitions), Variables: values[app.Name]})
	}
	return apps
}

func declaredFor(app variables.App, definitions []*resourcesv1.VariableDefinition) []*resourcesv1.VariableDefinition {
	var declared []*resourcesv1.VariableDefinition
	for _, definition := range definitions {
		if app.IsInScope(definition.GetFolders()) {
			declared = append(declared, definition)
		}
	}
	return declared
}

func PublicKeys(app variables.App, definitions []*resourcesv1.VariableDefinition) []string {
	if app.Framework != buildoutput.FrameworkNext {
		return nil
	}
	keys := []string{processenv.NextPublicURLEnvVar}
	for _, definition := range declaredFor(app, definitions) {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN && processenv.IsNextPublic(definition.GetKey()) {
			keys = append(keys, definition.GetKey())
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

type buildRecord struct {
	Digests map[string]map[string]string `json:"digests,omitempty"`
}

func Record(projectDir string, apps []App) error {
	record := buildRecord{Digests: make(map[string]map[string]string, len(apps))}
	for _, app := range apps {
		record.Digests[app.Name] = digests(app)
	}
	return writeRecord(projectDir, record)
}

func writeRecord(projectDir string, record buildRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := filepath.Join(projectDir, recordPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func CheckFresh(projectDir string, apps []App) error {
	record, err := readRecord(projectDir)
	if err != nil {
		return err
	}

	var missing, changed []string
	for _, app := range apps {
		recorded := record.Digests[app.Name]
		for key, digest := range digests(app) {
			built, ok := recorded[key]
			switch {
			case !ok:
				missing = append(missing, key)
			case built != digest:
				changed = append(changed, key)
			}
		}
	}
	if len(missing) == 0 && len(changed) == 0 {
		return nil
	}
	slices.Sort(missing)
	slices.Sort(changed)

	var causes []string
	if len(missing) > 0 {
		causes = append(causes, fmt.Sprintf(
			"%s %s never inlined — either not a public variable when "+statedir.Name+"/output was built, or built by `ocel build`, which resolves no values",
			strings.Join(missing, ", "), were(missing),
		))
	}
	if len(changed) > 0 {
		causes = append(causes, fmt.Sprintf("the public value of %s changed since "+statedir.Name+"/output was built", strings.Join(changed, ", ")))
	}
	return fmt.Errorf(
		"--prebuilt cannot deploy this build: %s. "+
			"A public value is inlined into the browser bundle at build time, so this deploy would serve browsers something other than what its server resolves. "+
			"Deploy without --prebuilt to build with the values this deploy resolved",
		strings.Join(causes, ", and "),
	)
}

func were(keys []string) string {
	if len(keys) == 1 {
		return "was"
	}
	return "were"
}

func readRecord(projectDir string) (buildRecord, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, recordPath))
	if errors.Is(err, fs.ErrNotExist) {
		return buildRecord{}, nil
	}
	if err != nil {
		return buildRecord{}, err
	}
	var record buildRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return buildRecord{}, fmt.Errorf("read %s: %w", recordPath, err)
	}
	return record, nil
}

func digests(app App) map[string]string {
	out := map[string]string{}
	values := make(map[string]string, len(app.Variables))
	for _, v := range app.Variables {
		values[v.Key] = v.Value
	}
	for _, key := range PublicKeys(app.App, app.Declared) {
		value, resolved := values[key]
		if !resolved {
			continue
		}
		sum := sha256.Sum256([]byte(value))
		out[key] = hex.EncodeToString(sum[:])
	}
	return out
}
