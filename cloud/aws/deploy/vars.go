package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

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
// the function's configuration entirely, so reading the configuration
// discloses nothing that was meant to be encrypted. The app's folder binding
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

// DataKeyMaker is the subset of KMS the encrypted-baked render needs: one call
// yielding a data key and the same key wrapped under the substrate's class
// key. The aws-sdk-go-v2 KMS client satisfies it; tests substitute a fake.
type DataKeyMaker interface {
	GenerateDataKey(ctx context.Context, in *kms.GenerateDataKeyInput, optFns ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
}

// bakedBundle is one app's encrypted-baked values, split across the two places
// they are delivered: the ciphertext rides inside every one of the app's
// function packages, and the wrapped data key — which discloses nothing on its
// own — is the single entry they contribute to function configuration. The
// zero bundle is an app that declared none.
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
// name. A deploy where no app declares one never touches KMS, which is what
// lets the provider be configured without a key maker until a project needs
// the class.
func renderBakedBundles(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) (map[string]bakedBundle, error) {
	bundles := make(map[string]bakedBundle, len(manifest.GetApps()))
	for _, app := range manifest.GetApps() {
		bundle, err := renderBakedBundle(ctx, cfg.VarsKMS, cfg.VarsKeyARN, app)
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
// generated for this deploy and wrapped by the substrate's own class key. A
// fresh key each time is what keeps preview compute unable to open production
// ciphertext and what makes every rotation a distinct artifact.
func renderBakedBundle(ctx context.Context, km DataKeyMaker, keyARN string, app *deploymentsv1.ManifestApp) (bakedBundle, error) {
	values := make(map[string]string)
	for _, v := range app.GetVariables() {
		if v.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE {
			values[v.GetKey()] = v.GetValue()
		}
	}
	if len(values) == 0 {
		return bakedBundle{}, nil
	}
	if km == nil {
		return bakedBundle{}, fmt.Errorf("%s declares `sensitive` variables but this deploy was configured without a key to seal them under", app.GetName())
	}

	out, err := km.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId:   aws.String(keyARN),
		KeySpec: kmstypes.DataKeySpecAes256,
	})
	if err != nil {
		return bakedBundle{}, fmt.Errorf("generate a data key for %s's encrypted variables: %w", app.GetName(), err)
	}
	ciphertext, err := baked.Seal(out.Plaintext, values)
	if err != nil {
		return bakedBundle{}, fmt.Errorf("seal %s's encrypted variables: %w", app.GetName(), err)
	}
	return bakedBundle{
		Envelope:   base64.StdEncoding.EncodeToString(out.CiphertextBlob),
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
	b.WriteString("\n\nReclassify a variable as `sensitive` or `secret` to deliver it outside the function environment.")
	return fmt.Errorf("%s", b.String())
}
