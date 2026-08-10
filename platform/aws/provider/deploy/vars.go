package deploy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/baked"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const functionEnvBudgetBytes = 4096

func varsReadPolicy(r executionRole) (string, error) {
	statements := []any{
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"kms:Decrypt"},
			"Resource": r.VarsKeyARN,
		},
	}
	if r.VarsTableARN != "" {
		partitions := []string{vars.PartitionKey(r.Slug, r.VarsClass)}
		for _, owner := range r.VarsReferenced {
			if owner != r.Slug {
				partitions = append(partitions, vars.PartitionKey(owner, r.VarsClass))
			}
		}
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"dynamodb:Query"},
			"Resource": r.VarsTableARN,
			"Condition": map[string]any{
				"ForAllValues:StringEquals": map[string]any{
					"dynamodb:LeadingKeys": partitions,
				},
			},
		})
	}
	out, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render vars read policy: %w", err)
	}
	return string(out), nil
}

const appFolderEnv = "OCEL_APP_FOLDER"

func variableEnv(app *deploymentsv1.ManifestApp) map[string]string {
	env := make(map[string]string)
	for _, v := range app.GetVariables() {
		if v.GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			continue
		}
		env[v.GetKey()] = v.GetValue()
	}
	if folder := app.GetFolder(); folder != "" {
		env[appFolderEnv] = folder
	}
	return env
}

type appBundle struct {
	Envelope    string
	Ciphertext  []byte
	Live        []byte
	Referenced  []string
	Fingerprint string
}

func (b appBundle) env() map[string]string {
	if b.Envelope == "" {
		return nil
	}
	return map[string]string{baked.EnvelopeVar: b.Envelope}
}

func (b appBundle) overlay() map[string][]byte {
	files := map[string][]byte{}
	if len(b.Ciphertext) > 0 {
		files[baked.FilePath] = b.Ciphertext
	}
	if len(b.Live) > 0 {
		files[live.FilePath] = b.Live
	}
	if len(files) == 0 {
		return nil
	}
	return files
}

func (b appBundle) hasLive() bool { return len(b.Live) > 0 }

func renderAppBundles(cfg Config, manifest *deploymentsv1.Manifest) (map[string]appBundle, error) {
	bundles := make(map[string]appBundle, len(manifest.GetApps()))
	for _, app := range manifest.GetApps() {
		bundle, err := renderAppBundle(cfg, manifest.GetSlug(), app)
		if err != nil {
			return nil, err
		}
		if bundle.Envelope != "" || bundle.hasLive() {
			bundles[app.GetName()] = bundle
		}
	}
	return bundles, nil
}

func renderAppBundle(cfg Config, slug string, app *deploymentsv1.ManifestApp) (appBundle, error) {
	values := make(map[string]string)
	var keys []live.Key
	for _, v := range app.GetVariables() {
		switch v.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			values[v.GetKey()] = v.GetValue()
		case resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
			keys = append(keys, live.Key{Key: v.GetKey(), Folder: v.GetFolder()})
		default:
			return appBundle{}, fmt.Errorf("%s declares %s with class %s, which this deploy cannot deliver to a function yet; declare it as `plain` or `sensitive`", app.GetName(), v.GetKey(), v.GetClass())
		}
	}

	manifest, err := live.Render(live.Manifest{
		Slug:        slug,
		Table:       cfg.VarsTable,
		KeyARN:      cfg.VarsKeyARN,
		Class:       cfg.VarsClass,
		Environment: overrideEnvironment(cfg),
		Keys:        keys,
	})
	if err != nil {
		return appBundle{}, fmt.Errorf("pin %s's live values: %w", app.GetName(), err)
	}

	referenced := referencedOwners(cfg, slug, keys)
	if len(values) == 0 {
		return appBundle{Live: manifest, Referenced: referenced}, nil
	}

	key := make([]byte, baked.KeyBytes)
	if _, err := rand.Read(key); err != nil {
		return appBundle{}, fmt.Errorf("generate a data key for %s's encrypted variables: %w", app.GetName(), err)
	}
	ciphertext, err := baked.Seal(key, values)
	if err != nil {
		return appBundle{}, fmt.Errorf("seal %s's encrypted variables: %w", app.GetName(), err)
	}
	return appBundle{
		Envelope:    base64.StdEncoding.EncodeToString(key),
		Ciphertext:  ciphertext,
		Live:        manifest,
		Referenced:  referenced,
		Fingerprint: fingerprintValues(values),
	}, nil
}

func referencedOwners(cfg Config, slug string, keys []live.Key) []string {
	environments := []string{""}
	if environment := overrideEnvironment(cfg); environment != "" {
		environments = append(environments, environment)
	}

	owners := map[string]bool{}
	for _, key := range keys {
		for _, environment := range environments {
			cell := vars.Coordinate{Slug: slug, Folder: key.Folder, Key: key.Key, Environment: environment}
			if owner := cfg.VarsReferenced[cell]; owner != "" {
				owners[owner] = true
			}
		}
	}

	out := make([]string, 0, len(owners))
	for owner := range owners {
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

func overrideEnvironment(cfg Config) string {
	if cfg.Class != deploymentsv1.Environment_CLASS_PREVIEW {
		return ""
	}
	return cfg.Identity
}

func recordedAudit(cfg Config, app *deploymentsv1.ManifestApp) (string, []edge.VariableRecord) {
	if cfg.Class != deploymentsv1.Environment_CLASS_PRODUCTION {
		return "", nil
	}
	var records []edge.VariableRecord
	for _, v := range app.GetVariables() {
		record := edge.VariableRecord{Key: v.GetKey(), Folder: v.GetFolder()}
		if v.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			record.Live = true
		} else {
			record.Version = v.GetVersion()
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Key != records[j].Key {
			return records[i].Key < records[j].Key
		}
		return records[i].Folder < records[j].Folder
	})
	return fingerprintRecords(records), records
}

func fingerprintRecords(records []edge.VariableRecord) string {
	if len(records) == 0 {
		return ""
	}
	h := sha256.New()
	for _, record := range records {
		writeLenPrefixed(h, []byte(record.Key))
		writeLenPrefixed(h, []byte(record.Folder))
		if record.Live {
			writeLenPrefixed(h, []byte(liveVersionMarker))
			continue
		}
		writeLenPrefixed(h, []byte(strconv.FormatInt(record.Version, 10)))
	}
	return hex.EncodeToString(h.Sum(nil))[:fingerprintValuesHexLen]
}

const liveVersionMarker = "live"

const fingerprintValuesHexLen = 12

func fingerprintValues(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, key := range keys {
		writeLenPrefixed(h, []byte(key))
		writeLenPrefixed(h, []byte(values[key]))
	}
	return hex.EncodeToString(h.Sum(nil))[:fingerprintValuesHexLen]
}

var runtimeOwnedPrefixes = []string{"AWS_", "LAMBDA_"}

func checkRuntimeOwnedNames(app *deploymentsv1.ManifestApp) error {
	var taken []string
	for _, v := range app.GetVariables() {
		if v.GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			continue
		}
		for _, prefix := range runtimeOwnedPrefixes {
			if strings.HasPrefix(v.GetKey(), prefix) {
				taken = append(taken, v.GetKey())
				break
			}
		}
	}
	if len(taken) == 0 {
		return nil
	}
	sort.Strings(taken)

	return fmt.Errorf(
		"app %s declares %s, which the AWS Lambda runtime injects into every function environment (%s). "+
			"A plaintext variable is delivered under its own name, so the runtime would overwrite it. "+
			"Rename it, or reclassify it as `sensitive` to deliver it inside the bundle instead",
		app.GetName(), strings.Join(taken, ", "), strings.Join(runtimeOwnedPrefixes, ", "),
	)
}

func checkFunctionEnvBudget(function string, env map[string]string) error {
	total := 0
	keys := make([]string, 0, len(env))
	for key, value := range env {
		total += len(key) + len(value)
		keys = append(keys, key)
	}
	if total <= functionEnvBudgetBytes {
		return nil
	}

	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if sizeOf := len(a) + len(env[a]); sizeOf != len(b)+len(env[b]) {
			return sizeOf > len(b)+len(env[b])
		}
		return a < b
	})

	var b strings.Builder
	fmt.Fprintf(&b, "the environment for function %s is %d bytes, over the %d-byte limit:\n", function, total, functionEnvBudgetBytes)
	for _, key := range keys {
		fmt.Fprintf(&b, "\n  %s  %d bytes", key, len(key)+len(env[key]))
	}
	b.WriteString("\n\nReclassify a variable as `sensitive` to deliver it as ciphertext inside the bundle instead of in the function environment.")
	return fmt.Errorf("%s", b.String())
}
