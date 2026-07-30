package deploy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ocelhq/ocel/cloud/aws/vars/baked"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// functionEnvBudgetBytes is AWS Lambda's ceiling on a function's environment:
// 4KB across every key and value together. Variables share it with the
// resource payloads already there, so it is accounted once, over the whole
// set, at deploy — a deploy that would exceed it fails here rather than at
// AWS, where the message names no key.
const functionEnvBudgetBytes = 4096

// varsDecryptPolicy grants a function execution role decrypt on exactly one
// key: the one its own env class's variable store encrypts under, named by ARN
// rather than a wildcard so the class boundary holds. Decrypt is the only
// action — values are encrypted provider-side, never by a runtime.
func varsDecryptPolicy(keyARN string) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"kms:Decrypt"},
				"Resource": keyARN,
			},
		},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render vars decrypt policy: %w", err)
	}
	return string(out), nil
}

// appFolderEnv carries the folder the app binds, which is what its variables
// resolved from. The runtime reads it only to explain a key scoped to another
// folder; the values themselves arrive already resolved. It is left unset for
// an unbound app, because the project root is the absence of a binding and a
// second spelling of it would be one the reader has to know about.
const appFolderEnv = "OCEL_APP_FOLDER"

// variableEnv is the environment entries an app's resolved variables
// contribute: the plaintext class only, under the bare key the user chose,
// because interop with code that reads the process environment itself is the
// property that distinguishes that class. Every other class is delivered off
// the function's configuration entirely, so a value meant to be encrypted is
// never legible in a configuration listing. Any class this deploy cannot
// deliver has already failed the deploy in renderBakedBundle, so the filter
// here can never be the thing that drops a value. The app's folder binding
// rides along because it is what decided every one of those resolutions.
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

// bakedBundle is one app's encrypted-baked values, split across the two places
// they are delivered: the ciphertext rides inside every one of the app's
// function packages, and the data key it was sealed under is the single entry
// they contribute to function configuration. The zero bundle is an app that
// declared none.
type bakedBundle struct {
	Envelope   string
	Ciphertext []byte
}

// env is the bundle's contribution to function configuration. It is accounted
// against the environment budget like anything else, and it is one entry
// however many values the app bakes.
func (b bakedBundle) env() map[string]string {
	if b.Envelope == "" {
		return nil
	}
	return map[string]string{baked.EnvelopeVar: b.Envelope}
}

// overlay is the file the bundle adds to each of the app's function packages.
// It is folded into the package's content hash, so rotating a value lands as a
// new artifact rather than silently reusing the one holding the old ciphertext.
func (b bakedBundle) overlay() map[string][]byte {
	if len(b.Ciphertext) == 0 {
		return nil
	}
	return map[string][]byte{baked.FilePath: b.Ciphertext}
}

// renderBakedBundles seals every app's encrypted-baked values, keyed by app
// name, and is where a variable whose class this deploy path cannot deliver
// stops the deploy — before anything is packaged or provisioned.
func renderBakedBundles(_ context.Context, _ Config, manifest *deploymentsv1.Manifest) (map[string]bakedBundle, error) {
	bundles := make(map[string]bakedBundle, len(manifest.GetApps()))
	for _, app := range manifest.GetApps() {
		bundle, err := renderBakedBundle(app)
		if err != nil {
			return nil, err
		}
		if bundle.Envelope != "" {
			bundles[app.GetName()] = bundle
		}
	}
	return bundles, nil
}

// renderBakedBundle seals an app's encrypted-baked values under a data key
// drawn fresh for this render, which is what keeps one deployment's artifact
// unopenable by another's configuration and makes every rotation a distinct
// artifact. The key travels in the function's configuration rather than under
// a substrate key, so the membrane opens the bundle with what it already has:
// see the changeset for what that does and does not protect against.
//
// A class this path has no delivery for fails here rather than being skipped,
// because a skipped variable is a deploy that reports success and an
// application whose value is simply absent.
func renderBakedBundle(app *deploymentsv1.ManifestApp) (bakedBundle, error) {
	values := make(map[string]string)
	for _, v := range app.GetVariables() {
		switch v.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			values[v.GetKey()] = v.GetValue()
		default:
			return bakedBundle{}, fmt.Errorf("%s declares %s with class %s, which this deploy cannot deliver to a function yet; declare it as `plain` or `sensitive`", app.GetName(), v.GetKey(), v.GetClass())
		}
	}
	if len(values) == 0 {
		return bakedBundle{}, nil
	}

	key := make([]byte, baked.KeyBytes)
	if _, err := rand.Read(key); err != nil {
		return bakedBundle{}, fmt.Errorf("generate a data key for %s's encrypted variables: %w", app.GetName(), err)
	}
	ciphertext, err := baked.Seal(key, values)
	if err != nil {
		return bakedBundle{}, fmt.Errorf("seal %s's encrypted variables: %w", app.GetName(), err)
	}
	return bakedBundle{
		Envelope:   base64.StdEncoding.EncodeToString(key),
		Ciphertext: ciphertext,
	}, nil
}

// checkFunctionEnvBudget refuses a function environment that does not fit the
// platform's limit, naming what each key costs and the remedy: moving a value
// to an encrypted class takes it out of the function environment altogether.
// The alternative — spilling over to another delivery path — would silently
// change a value's confidentiality, which is the one thing a class must never
// do behind the user's back.
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
