package deploy

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/baked"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
		for _, link := range r.VarsLinks {
			partitions = append(partitions, vars.LinkPartitionKey(r.Slug, r.VarsClass, link))
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

func appEnv(manifest *deploymentsv1.Manifest, app *deploymentsv1.ManifestApp, bundle appBundle, cfg Config, sessions sessionScope) map[string]string {
	env := map[string]string{}
	if cfg.Edge != nil {
		env[edgeKindEnv] = string(cfg.Edge.Kind())
		facts := cfg.Edge.Facts()
		if !facts.RunsCode {
			env[edge.OriginRouterVar] = "1"
			env[edge.OriginSignedVar] = "1"
		}
		if facts.InvalidatesByCacheTag {
			env[edge.CacheTagPurgeVar] = "1"
		}
	}
	if appCrossesMembrane(manifest, app.GetName()) {
		env[envStateTable] = cfg.StateTable
		env[envSessionPrefix] = sessions.KeyPrefix
	}
	maps.Copy(env, variableEnv(app))
	maps.Copy(env, bundle.env())
	return env
}

type appBundle struct {
	Envelope    string
	Ciphertext  []byte
	Live        []byte
	Referenced  []string
	Links       []string
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

func renderAppBundles(cfg Config, manifest *deploymentsv1.Manifest, consumed map[string]Consumed) (map[string]appBundle, error) {
	bundles := make(map[string]appBundle, len(manifest.GetApps()))
	for _, app := range manifest.GetApps() {
		bundle, err := renderAppBundle(cfg, manifest.GetSlug(), app, appLinks(manifest, app.GetName(), consumed))
		if err != nil {
			return nil, err
		}
		if bundle.Envelope != "" || bundle.hasLive() {
			bundles[app.GetName()] = bundle
		}
	}
	return bundles, nil
}

func renderAppBundle(cfg Config, slug string, app *deploymentsv1.ManifestApp, links []live.Link) (appBundle, error) {
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
		Links:       links,
	})
	if err != nil {
		return appBundle{}, fmt.Errorf("pin %s's live values: %w", app.GetName(), err)
	}

	referenced := referencedOwners(cfg, slug, keys)
	linked := linkNames(links)
	if len(values) == 0 {
		return appBundle{Live: manifest, Referenced: referenced, Links: linked}, nil
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
		Links:       linked,
		Fingerprint: fingerprintValues(values),
	}, nil
}

func linkNames(links []live.Link) []string {
	names := make([]string, 0, len(links))
	for _, l := range links {
		names = append(names, l.Name)
	}
	slices.Sort(names)
	return names
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
	slices.Sort(out)
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
	slices.SortFunc(records, func(a, b edge.VariableRecord) int {
		if c := cmp.Compare(a.Key, b.Key); c != 0 {
			return c
		}
		return cmp.Compare(a.Folder, b.Folder)
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
	slices.Sort(keys)

	h := sha256.New()
	for _, key := range keys {
		writeLenPrefixed(h, []byte(key))
		writeLenPrefixed(h, []byte(values[key]))
	}
	return hex.EncodeToString(h.Sum(nil))[:fingerprintValuesHexLen]
}

var runtimeOwnedPrefixes = []string{"AWS_", "LAMBDA_"}

func plainNamesTaken(app *deploymentsv1.ManifestApp, owned func(string) bool) []string {
	var taken []string
	for _, v := range app.GetVariables() {
		if v.GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			continue
		}
		if owned(v.GetKey()) {
			taken = append(taken, v.GetKey())
		}
	}
	slices.Sort(taken)
	return taken
}

func checkRuntimeOwnedNames(app *deploymentsv1.ManifestApp) error {
	taken := plainNamesTaken(app, func(key string) bool {
		for _, prefix := range runtimeOwnedPrefixes {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
		return false
	})
	if len(taken) == 0 {
		return nil
	}

	return fmt.Errorf(
		"app %s declares %s, which the AWS Lambda runtime injects into every function environment (%s). "+
			"A plaintext variable is delivered under its own name, so the runtime would overwrite it. "+
			"Rename it, or reclassify it as `sensitive` to deliver it inside the bundle instead",
		app.GetName(), strings.Join(taken, ", "), strings.Join(runtimeOwnedPrefixes, ", "),
	)
}

func checkEdgeOwnedNames(app *deploymentsv1.ManifestApp) error {
	taken := plainNamesTaken(app, func(key string) bool {
		return slices.Contains(edge.OwnedVariableNames, key) || strings.HasPrefix(key, baked.Prefix)
	})
	if len(taken) == 0 {
		return nil
	}

	return fmt.Errorf(
		"app %s declares %s, which the edge entry worker injects into every worker environment (%s, %s*). "+
			"A plaintext variable is delivered under its own name, so the entry worker would overwrite it. "+
			"Rename it, or reclassify it as `sensitive` to deliver it inside the sealed overlay instead",
		app.GetName(), strings.Join(taken, ", "), strings.Join(edge.OwnedVariableNames, ", "), baked.Prefix,
	)
}

func checkEdgeVariables(app *deploymentsv1.ManifestApp, bundle appBundle) error {
	if err := checkEdgeOwnedNames(app); err != nil {
		return err
	}
	return checkEdgeEnvBudget(app.GetName(), variableEnv(app), bundle.Ciphertext)
}

func envBudget(env map[string]string) (int, []string) {
	total := 0
	keys := make([]string, 0, len(env))
	for key, value := range env {
		total += len(key) + len(value)
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := cmp.Compare(len(b)+len(env[b]), len(a)+len(env[a])); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	return total, keys
}

func writeEnvBudgetEntries(b *strings.Builder, env map[string]string, keys []string) {
	for _, key := range keys {
		fmt.Fprintf(b, "\n  %s  %d bytes", key, len(key)+len(env[key]))
	}
}

func checkFunctionEnvBudget(function string, env map[string]string) error {
	total, keys := envBudget(env)
	if total <= functionEnvBudgetBytes {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "the environment for function %s is %d bytes, over the %d-byte limit:\n", function, total, functionEnvBudgetBytes)
	writeEnvBudgetEntries(&b, env, keys)
	b.WriteString("\n\nReclassify a variable as `sensitive` to deliver it as ciphertext inside the bundle instead of in the function environment.")
	return fmt.Errorf("%s", b.String())
}

func checkEdgeEnvBudget(app string, env map[string]string, ciphertext []byte) error {
	total, keys := envBudget(env)
	total += len(ciphertext)
	if total <= functionEnvBudgetBytes {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "the environment for app %s at the edge is %d bytes, over the %d-byte limit:\n", app, total, functionEnvBudgetBytes)
	writeEnvBudgetEntries(&b, env, keys)
	if len(ciphertext) > 0 {
		fmt.Fprintf(&b, "\n  %s  %d bytes", edgeSealedFile, len(ciphertext))
	}
	b.WriteString("\n\nDrop a variable or shorten a value: the edge carries plaintext and `sensitive` variables under one budget.")
	return fmt.Errorf("%s", b.String())
}
