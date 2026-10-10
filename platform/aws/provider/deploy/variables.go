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
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/variablestore"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/variables/baked"
	variables "github.com/ocelhq/ocel/platform/aws/provider/variables/live"
)

const functionEnvBudgetBytes = 4096

func variablesReadPolicy(r executionRole) (string, error) {
	var statements []any
	if r.VariablesKeyARN != "" {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"kms:Decrypt"},
			"Resource": r.VariablesKeyARN,
		})
	}
	if r.ValuesTableARN != "" {
		partitions := []string{valuePartition(r.Slug, r.VariablesTier)}
		for _, owner := range r.VariablesReferenced {
			if owner == r.Slug {
				continue
			}
			partitions = append(partitions, valuePartition(owner, r.VariablesTier))
		}
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"dynamodb:Query"},
			"Resource": r.ValuesTableARN,
			"Condition": map[string]any{
				"ForAllValues:StringEquals": map[string]any{
					"dynamodb:LeadingKeys": partitions,
				},
			},
		})
	}
	out, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render variables read policy: %w", err)
	}
	return string(out), nil
}

func valuePartition(slug, tier string) string {
	return awsports.PartitionKey(variablestore.ValuesPartition(variablestore.Scope{Project: slug, Tier: environment.Tier(tier)}))
}

type appBundle struct {
	Envelope    string
	Ciphertext  []byte
	Live        []byte
	Queues      []byte
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
		files[variables.FilePath] = b.Live
	}
	if len(b.Queues) > 0 {
		files[queues.FilePath] = b.Queues
	}
	if len(files) == 0 {
		return nil
	}
	return files
}

func (b appBundle) hasLive() bool { return len(b.Live) > 0 }

func sealAppBundle(cfg Config, slug, app string, sensitive map[string]string, keys []live.Key, bindings []live.Binding) (appBundle, error) {
	manifest, err := variables.Render(variables.Manifest{
		Slug:        slug,
		Table:       cfg.VariablesTable,
		KeyARN:      cfg.VariablesKeyARN,
		Tier:        string(cfg.Tier),
		Environment: overrideEnvironment(cfg),
		Keys:        keys,
		Bindings:    bindings,
	})
	if err != nil {
		return appBundle{}, fmt.Errorf("pin %s's live values: %w", app, err)
	}

	referenced := referencedOwners(cfg, slug, keys)
	if len(sensitive) == 0 {
		return appBundle{Live: manifest, Referenced: referenced}, nil
	}
	if cfg.VariablesKeyARN == "" {
		return appBundle{}, fmt.Errorf("app %s declares sensitive variables, and this account names no key to encrypt their data key under in the function's environment; bootstrap the variables-key feature first", app)
	}

	key := make([]byte, baked.KeyBytes)
	if _, err := rand.Read(key); err != nil {
		return appBundle{}, fmt.Errorf("generate a data key for %s's encrypted variables: %w", app, err)
	}
	ciphertext, err := baked.Seal(key, sensitive)
	if err != nil {
		return appBundle{}, fmt.Errorf("seal %s's encrypted variables: %w", app, err)
	}
	return appBundle{
		Envelope:    base64.StdEncoding.EncodeToString(key),
		Ciphertext:  ciphertext,
		Live:        manifest,
		Referenced:  referenced,
		Fingerprint: fingerprintValues(sensitive),
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
			cell := variablestore.Coordinate{Cell: variablestore.Cell{Folder: key.Folder, Key: key.Key}, Environment: environment}
			if owner := cfg.VariablesReferenced[cell]; owner != "" {
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
	if cfg.Tier != environment.TierPreview {
		return ""
	}
	return cfg.Env
}

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
		provider.WriteLenPrefixed(h, []byte(key))
		provider.WriteLenPrefixed(h, []byte(values[key]))
	}
	return hex.EncodeToString(h.Sum(nil))[:provider.FingerprintHexLen]
}

var runtimeOwnedPrefixes = []string{"AWS_", "LAMBDA_"}

func deliveredNamesTaken(values provider.AppValues, owned func(string) bool) []string {
	var taken []string
	for _, delivered := range []map[string]string{values.Plain, values.Sensitive} {
		for key := range delivered {
			if owned(key) {
				taken = append(taken, key)
			}
		}
	}
	slices.Sort(taken)
	return taken
}

func checkRuntimeOwnedNames(app string, values provider.AppValues) error {
	taken := deliveredNamesTaken(values, func(key string) bool {
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
			"Every variable is delivered under its own name, so it would collide with the runtime's own value. Rename it",
		app, strings.Join(taken, ", "), strings.Join(runtimeOwnedPrefixes, ", "),
	)
}

func checkEdgeOwnedNames(app string, values provider.AppValues) error {
	taken := deliveredNamesTaken(values, func(key string) bool {
		return slices.Contains(edge.OwnedVariableNames, key)
	})
	if len(taken) == 0 {
		return nil
	}

	return fmt.Errorf(
		"app %s declares %s, which the edge entry worker injects into every worker environment (%s). "+
			"Every variable is delivered under its own name, so the entry worker would overwrite it. Rename it",
		app, strings.Join(taken, ", "), strings.Join(edge.OwnedVariableNames, ", "),
	)
}

func checkEdgeVariables(app string, values provider.AppValues, ciphertext []byte) error {
	if err := checkEdgeOwnedNames(app, values); err != nil {
		return err
	}
	return checkEdgeEnvBudget(app, plainEnv(values), ciphertext)
}

func plainEnv(values provider.AppValues) map[string]string {
	env := make(map[string]string, len(values.Plain)+1)
	maps.Copy(env, values.Plain)
	if values.Folder != "" {
		env[processenv.AppFolderEnvVar] = values.Folder
	}
	return env
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
	b.WriteString("\n\nDrop a variable or shorten a value: the edge fits plaintext and `sensitive` variables into one budget.")
	return fmt.Errorf("%s", b.String())
}
