package variables

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
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

var clientRecordPath = filepath.Join(buildoutput.StateDir, "output", "client-digests.json")

type ClientApp struct {
	App
	Declared  []*resourcesv1.VariableDefinition
	Variables []Variable
}

func ClientAppsOf(cfg *project.Project, definitions []*resourcesv1.VariableDefinition, values map[string][]Variable) []ClientApp {
	scoped := AppsOf(cfg)
	apps := make([]ClientApp, 0, len(scoped))
	for _, app := range scoped {
		apps = append(apps, ClientApp{App: app, Declared: declaredForApp(app, definitions), Variables: values[app.Name]})
	}
	return apps
}

func declaredForApp(app App, definitions []*resourcesv1.VariableDefinition) []*resourcesv1.VariableDefinition {
	var declared []*resourcesv1.VariableDefinition
	for _, definition := range definitions {
		if app.IsInScope(definition.GetFolders()) {
			declared = append(declared, definition)
		}
	}
	return declared
}

func PublicKeys(app App, definitions []*resourcesv1.VariableDefinition) []string {
	if app.Framework != buildoutput.FrameworkNext {
		return nil
	}
	keys := []string{processenv.NextPublicURLEnvVar}
	for _, definition := range declaredForApp(app, definitions) {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN && processenv.IsNextPublic(definition.GetKey()) {
			keys = append(keys, definition.GetKey())
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func inlinedKeys(app ClientApp) []string {
	switch app.Framework {
	case buildoutput.FrameworkNext:
		return PublicKeys(app.App, app.Declared)
	case buildoutput.FrameworkSvelteKit:
		var keys []string
		for _, definition := range app.Declared {
			if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
				keys = append(keys, definition.GetKey())
			}
		}
		return keys
	}
	return nil
}

type clientRecord struct {
	Tier        environmentv1.Tier           `json:"tier,omitempty"`
	Environment string                       `json:"environment,omitempty"`
	Digests     map[string]map[string]string `json:"digests,omitempty"`
}

func RecordClientValues(projectDir string, builtFor *environmentv1.Environment, apps []ClientApp) error {
	record := clientRecord{Tier: builtFor.GetTier(), Environment: builtFor.GetIdentity(), Digests: make(map[string]map[string]string, len(apps))}
	for _, app := range apps {
		record.Digests[app.Name] = clientDigests(app)
	}
	return writeClientRecord(projectDir, record)
}

func writeClientRecord(projectDir string, record clientRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := filepath.Join(projectDir, clientRecordPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func CheckClientValuesFresh(projectDir string, deployedTo *environmentv1.Environment, apps []ClientApp) error {
	record, err := readClientRecord(projectDir)
	if err != nil {
		return err
	}
	if err := refuseOtherEnvironment(record, deployedTo); err != nil {
		return err
	}

	var missing, changed []string
	for _, app := range apps {
		recorded := record.Digests[app.Name]
		current := clientDigests(app)
		for key, built := range recorded {
			if _, selected := current[key]; !selected && built != unsetClientDigest {
				changed = append(changed, key)
			}
		}
		for key, digest := range current {
			built, ok := recorded[key]
			switch {
			case !ok && digest == unsetClientDigest:
			case !ok, built == unsetClientDigest && digest != unsetClientDigest:
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
			"%s %s never inlined — either unset or not a variable the app inlines when "+buildoutput.StateDir+"/output was built, or built by `ocel build` without --env, which resolves no values",
			strings.Join(missing, ", "), were(missing),
		))
	}
	if len(changed) > 0 {
		causes = append(causes, fmt.Sprintf("the build-time value of %s changed since "+buildoutput.StateDir+"/output was built", strings.Join(changed, ", ")))
	}
	return fmt.Errorf(
		"--prebuilt cannot deploy this build: %s. "+
			"A value inlined at build time stays in the bundle, so this deploy would serve something other than what it resolved. "+
			"Deploy without --prebuilt to build with the values this deploy resolved",
		strings.Join(causes, ", and "),
	)
}

func refuseOtherEnvironment(record clientRecord, deployedTo *environmentv1.Environment) error {
	if record.Tier == environmentv1.Tier_TIER_UNSPECIFIED || record.Tier == deployedTo.GetTier() && record.Environment == deployedTo.GetIdentity() {
		return nil
	}
	return fmt.Errorf(
		"--prebuilt cannot deploy this build to %s: it was built with the values of %s, which its bundles and images may hold. "+
			"Deploy without --prebuilt to build with the values this deploy resolved",
		placeOf(deployedTo.GetTier(), deployedTo.GetIdentity()), placeOf(record.Tier, record.Environment),
	)
}

func placeOf(tier environmentv1.Tier, identity string) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "preview " + identity
	}
	return string(environment.TierProduction)
}

func were(keys []string) string {
	if len(keys) == 1 {
		return "was"
	}
	return "were"
}

func readClientRecord(projectDir string) (clientRecord, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, clientRecordPath))
	if errors.Is(err, fs.ErrNotExist) {
		return clientRecord{}, nil
	}
	if err != nil {
		return clientRecord{}, err
	}
	var record clientRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return clientRecord{}, fmt.Errorf("read %s: %w", clientRecordPath, err)
	}
	return record, nil
}

const unsetClientDigest = "unset"

func clientDigests(app ClientApp) map[string]string {
	out := map[string]string{}
	values := make(map[string]string, len(app.Variables))
	for _, v := range app.Variables {
		values[v.Key] = v.Value
	}
	for _, key := range inlinedKeys(app) {
		value, resolved := values[key]
		if !resolved {
			out[key] = unsetClientDigest
			continue
		}
		sum := sha256.Sum256([]byte(value))
		out[key] = hex.EncodeToString(sum[:])
	}
	return out
}
