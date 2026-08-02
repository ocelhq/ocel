// Package clientenv is the browser half of a project's variables: the one
// place that knows how a client-accessible value reaches a browser bundle.
//
// The mechanism is the bundler's own. A value is exported to the app build
// under the name it was declared with, and the accessor generated into the app
// names that entry literally, so the bundler's static replacement does the
// inlining — no custom build-step transform to track across bundler versions.
//
// Which names a bundler will inline is the bundler's rule, and this package
// holds no opinion on it: a developer answers it by naming the variable
// (`NEXT_PUBLIC_APP_ID` under Next, `VITE_APP_ID` under Vite) and nothing here
// adds a prefix or strips one. That is what keeps a second bundler from being a
// change to this package. The price is that a name the bundler passes over
// cannot be caught here, so the accessor refuses to load rather than let a
// browser read undefined.
//
// Everything here is per app. The accessor names one app's keys and is
// resolved through that app's own tsconfig, so two apps resolving one key
// differently each inline their own value and neither build can write over the
// other's.
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
	"sort"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// specifier is what application code imports. It resolves to the SDK's
// throwing fallback until an app's tsconfig maps it at the generated file.
const specifier = "ocel/env/client"

// accessorPath is where the generated accessor lives, relative to the app. The
// paths entry pointing at it is derived from this (see accessorTarget), so
// there is one spelling of the path and not two.
var accessorPath = filepath.Join(".ocel", "env-client.ts")

// configFiles are the files a framework reads a paths mapping from, in the
// order an app is asked for one.
var configFiles = []string{"tsconfig.json", "jsconfig.json"}

// recordPath is where a build records the client values it inlined, relative
// to the project. It lives in the build output because that is what it
// describes: the output is reset per build, so a record found beside it was
// written by the build that produced it.
var recordPath = filepath.Join(".ocel", "output", "client-values.json")

// App is one app's client-accessible resolution. Name is the app the manifest
// carries, empty for the app a project configuring none deploys; Dir is the
// app's own directory, where its accessor and config live.
type App struct {
	Name      string
	Dir       string
	Variables []manifestbuilder.Variable
}

// Generate writes each app's accessor and points that app's `ocel/env/client`
// imports at it. An app declaring no client-accessible value is left entirely
// alone — no generated file, no edit to a config the developer maintains.
func Generate(apps []App) error {
	for _, app := range apps {
		if err := GenerateKeys(app.Dir, clientKeys(app)); err != nil {
			return err
		}
	}
	return nil
}

// GenerateKeys writes one directory's accessor for the keys named. It is what
// a caller with declarations and no per-app resolution behind them has: dev,
// where one flat dotfile answers every app, generates the same accessor a
// deploy does from the keys the run declared.
func GenerateKeys(dir string, keys []string) error {
	path := filepath.Join(dir, accessorPath)
	if len(keys) == 0 {
		// An app that had client values and no longer does keeps a truthful
		// accessor rather than a stale one; its config still points here.
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(accessorPath), err)
	}
	if err := os.WriteFile(path, []byte(accessor(keys)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", accessorPath, err)
	}
	return mapSpecifier(dir)
}

// inlinedHelper refuses a key nothing arrived under. A declared value always
// reaches the build, and a client-accessible key may carry no schema default
// for one to be confused with, so an absent value means one thing: the bundler
// passed the name over and the browser would read undefined. Checking at module
// load rather than at the read is what turns a silent undefined in the browser
// into a loud, named error the moment the module is imported, instead of a value
// that reads as undefined at some arbitrary later point.
const inlinedHelper = `const inlined = (key: string, value: string | undefined): string => {
  if (value !== undefined) return value;
  const error = new Error(
    "'" + key + "' is client-accessible, but no value was inlined into this bundle. A bundler inlines only the names matching its own public convention (NEXT_PUBLIC_* under Next, VITE_* under Vite); rename the variable to one of those, or read it on the server instead."
  );
  error.name = "EnvClientError";
  throw error;
};
`

// accessor is the generated module. Every value is a literal
// `process.env.<KEY>` member expression under the key's own declared name,
// because that is the only shape a bundler's static replacement rewrites: any
// lookup by variable would survive into the browser as a read of an environment
// that is not there. No prefix is added and none is stripped — which names get
// inlined is the bundler's rule, and the developer already answered it by
// naming the variable. The file is checked by the app's own tsconfig, so the
// helper is annotated: unannotated parameters are an error under the strict
// setting an app is scaffolded with, and the annotation is what makes each
// entry a `string` to the app rather than something it has to narrow.
func accessor(keys []string) string {
	var b strings.Builder
	b.WriteString("// Generated by ocel. Do not edit.\n")
	if len(keys) > 0 {
		b.WriteString(inlinedHelper + "\n")
	}
	b.WriteString("export const clientEnv = {\n")
	for _, key := range keys {
		b.WriteString("  " + key + `: inlined("` + key + `", process.env.` + key + "),\n")
	}
	b.WriteString("};\n")
	return b.String()
}

// mapSpecifier makes `ocel/env/client` resolve to the app's generated
// accessor, through the paths mapping the framework already honours. An app
// with no tsconfig or jsconfig has nowhere to state it and is left as it is:
// its imports land on the SDK's fallback, which refuses a read by name.
func mapSpecifier(dir string) error {
	for _, name := range configFiles {
		path := filepath.Join(dir, name)
		source, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}

		updated, err := withMapping(path, string(source))
		if err != nil {
			return err
		}
		if updated == string(source) {
			return nil
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		return nil
	}
	return nil
}

// buildRecord is what a build says about the client values its output carries.
// Resolved is the distinction a refusal has to make: a build that had no
// values to inline is a different situation, with a different remedy, from one
// whose values have since changed.
type buildRecord struct {
	Resolved bool                         `json:"resolved"`
	Inlined  map[string]map[string]string `json:"inlined,omitempty"`
}

// Record writes what the build that just ran inlined into its browser bundles,
// so a later --prebuilt deploy can tell whether reusing that output is honest.
// It holds a digest per key and never the value: the output travels.
func Record(projectDir string, apps []App) error {
	record := buildRecord{Resolved: true, Inlined: make(map[string]map[string]string, len(apps))}
	for _, app := range apps {
		record.Inlined[app.Name] = digests(app)
	}
	return writeRecord(projectDir, record)
}

// RecordUnresolved states that the build that just ran inlined nothing because
// it had nothing to inline. `ocel build` holds no provider session and so
// resolves no values at all — a supported flow, since a CI job can build in a
// container holding no credentials. Saying so is what lets a later --prebuilt
// deploy report the true cause rather than accusing a value of changing.
func RecordUnresolved(projectDir string) error {
	return writeRecord(projectDir, buildRecord{})
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

// CheckFresh refuses a --prebuilt deploy whose build output does not carry the
// client values this deploy resolved. A client value is a copy taken at build
// time, so reusing such an output would ship a Deployment whose server side
// and whose browser bundles disagree about one key, silently.
//
// It refuses either way and names which of the two it is: a value that changed
// since the build, or one the build never inlined at all.
func CheckFresh(projectDir string, apps []App) error {
	record, err := readRecord(projectDir)
	if err != nil {
		return err
	}

	var missing, changed []string
	for _, app := range apps {
		inlined := record.Inlined[app.Name]
		for key, digest := range digests(app) {
			built, ok := inlined[key]
			switch {
			case !record.Resolved || !ok:
				missing = append(missing, key)
			case built != digest:
				changed = append(changed, key)
			}
		}
	}
	if len(missing) == 0 && len(changed) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(changed)

	var causes []string
	if len(missing) > 0 {
		cause := "it was not client-accessible when .ocel/output was built"
		if !record.Resolved {
			cause = "`ocel build` resolves no values"
		}
		causes = append(causes, fmt.Sprintf("%s %s never inlined — %s", strings.Join(missing, ", "), were(missing), cause))
	}
	if len(changed) > 0 {
		causes = append(causes, fmt.Sprintf("the client-accessible value of %s changed since .ocel/output was built", strings.Join(changed, ", ")))
	}
	return fmt.Errorf(
		"--prebuilt cannot deploy this build: %s. "+
			"A client value is inlined into the browser bundle at build time, so this deploy would serve browsers something other than what its server holds. "+
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

// readRecord is what the output in .ocel/output says about itself. Nothing
// there is a build that recorded nothing, which is read the same way as one
// that resolved nothing: either way no client value was inlined.
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

// clientKeys are the keys of one app's client-accessible values, sorted so the
// same resolution always generates the same file. Client access is a property
// only the plaintext class can carry, and the build environment exports
// nothing else, so an accessor naming another class would read undefined.
func clientKeys(app App) []string {
	keys := make([]string, 0, len(app.Variables))
	for _, v := range app.Variables {
		if isClient(v) {
			keys = append(keys, v.Key)
		}
	}
	sort.Strings(keys)
	return keys
}

func isClient(v manifestbuilder.Variable) bool {
	return v.ClientAccessible && v.Class == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN
}

// digests is one app's client values, each reduced to a digest of what a build
// would inline for it.
func digests(app App) map[string]string {
	out := map[string]string{}
	for _, v := range app.Variables {
		if !isClient(v) {
			continue
		}
		sum := sha256.Sum256([]byte(v.Value))
		out[v.Key] = hex.EncodeToString(sum[:])
	}
	return out
}
