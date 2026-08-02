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

	"github.com/ocelhq/ocel/cloud/aws/vars"
	"github.com/ocelhq/ocel/cloud/aws/vars/baked"
	"github.com/ocelhq/ocel/cloud/aws/vars/live"
	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// functionEnvBudgetBytes is AWS Lambda's ceiling on a function's environment:
// 4KB across every key and value together. Variables share it with the
// resource payloads already there, so it is accounted once, over the whole
// set, at deploy — a deploy that would exceed it fails here rather than at
// AWS, where the message names no key.
const functionEnvBudgetBytes = 4096

// varsReadPolicy grants a function execution role what it needs to read its
// own live-class values and nothing more.
//
// Decrypt is on exactly one key: the one its own env class's variable store
// encrypts under, named by ARN rather than a wildcard so the class boundary
// holds. Encrypt is never granted — values are encrypted provider-side, never
// by a runtime.
//
// The table grant is added only for an app that actually declares a live value,
// and only over that project's own partition. The table is account-global and
// shared by every project in the class, so an unconditioned Query would let one
// function enumerate every project's ciphertext; the condition is built from
// vars.PartitionKey, the same function that builds the key it constrains, so
// the grant and the addressing cannot drift apart. Query is the only action:
// the runtime's read is one prefix query, and a point read it never emits is
// one more thing a compromised function could do.
func varsReadPolicy(keyARN, tableARN, slug, class string) (string, error) {
	statements := []any{
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"kms:Decrypt"},
			"Resource": keyARN,
		},
	}
	if tableARN != "" {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"dynamodb:Query"},
			"Resource": tableARN,
			"Condition": map[string]any{
				"ForAllValues:StringEquals": map[string]any{
					"dynamodb:LeadingKeys": []string{vars.PartitionKey(slug, class)},
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
// deliver has already failed the deploy in renderAppBundle, so the filter
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

// appBundle is what one app's variables add to its function packages and
// configuration, across the places each class is delivered to.
//
// The encrypted-baked half is two of them: the ciphertext rides inside every
// one of the app's function packages, and the data key it was sealed under is
// the single entry they contribute to function configuration.
//
// Live is the third, and the one that carries no value at all: the addresses of
// the app's live-class keys, which the membrane fetches through at runtime. It
// rides in the package rather than the configuration so a handful of
// coordinates does not compete for the environment budget.
//
// Fingerprint is the second half of the app's Deployment identity: a digest of
// the values sealed here, taken over the plaintext rather than the ciphertext
// the fresh data key makes different on every render. Empty when nothing is
// baked.
//
// The zero bundle is an app that declared neither.
type appBundle struct {
	Envelope    string
	Ciphertext  []byte
	Live        []byte
	Fingerprint string
}

// env is the bundle's contribution to function configuration. It is accounted
// against the environment budget like anything else, and it is one entry
// however many values the app bakes.
func (b appBundle) env() map[string]string {
	if b.Envelope == "" {
		return nil
	}
	return map[string]string{baked.EnvelopeVar: b.Envelope}
}

// overlay is the file the bundle adds to each of the app's function packages.
// It is folded into the package's content hash, so rotating a value lands as a
// new artifact rather than silently reusing the one holding the old ciphertext.
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

// hasLive reports whether the app reads the variable store at runtime, which is
// what decides whether its role is granted the table at all.
func (b appBundle) hasLive() bool { return len(b.Live) > 0 }

// renderAppBundles seals every app's encrypted-baked values, keyed by app
// name, and is where a variable whose class this deploy path cannot deliver
// stops the deploy — before anything is packaged or provisioned.
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

// renderAppBundle seals an app's encrypted-baked values under a data key
// drawn fresh for this render, which is what keeps one deployment's artifact
// unopenable by another's configuration and makes every rotation a distinct
// artifact. The key travels in the function's configuration rather than under
// a substrate key, so the membrane opens the bundle with what it already has:
// see the changeset for what that does and does not protect against.
//
// A class this path has no delivery for fails here rather than being skipped,
// because a skipped variable is a deploy that reports success and an
// application whose value is simply absent.
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
		Slug:   slug,
		Table:  cfg.VarsTable,
		KeyARN: cfg.VarsKeyARN,
		Class:  cfg.VarsClass,
		Keys:   keys,
	})
	if err != nil {
		return appBundle{}, fmt.Errorf("pin %s's live values: %w", app.GetName(), err)
	}

	if len(values) == 0 {
		return appBundle{Live: manifest}, nil
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
		Fingerprint: fingerprintValues(values),
	}, nil
}

// recordedVariables is what one app's Deployment record says it shipped with:
// every variable it resolved, at the store coordinate and version it resolved
// at, sorted by key so one deploy's record is comparable to another's.
//
// A live-class value is recorded as latest-at-runtime rather than at the
// version this deploy saw: it is fetched from the store when it is read, so a
// version here would be the ledger claiming a reproducibility the runtime
// cannot honour.
//
// Production only. The record is the audit ledger of what is serving the
// project's users, and a preview's Deployments are neither long-lived nor
// audited; keeping them out means one class of record to reason about rather
// than two. Nothing is lost by it: baked values ride the immutable artifact,
// so no rollback or delivery path reads this.
func recordedVariables(cfg Config, app *deploymentsv1.ManifestApp) []edge.VariableRecord {
	if cfg.Class != deploymentsv1.Environment_CLASS_PRODUCTION {
		return nil
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
	return records
}

// recordedFingerprint is the other half of the same audit record: a digest of
// every variable the Deployment shipped with, so any two promotions that
// shipped different values read differently in the ledger.
//
// It is deliberately not the identity's fingerprint. That one covers baked
// values alone — which is what keeps rotating a live value out of a redeploy —
// so an app whose every variable is live would fingerprint as nothing at all.
// This digest covers the live keys too, by their latest-at-runtime marker
// rather than by a version the ledger must never claim for them.
//
// Empty when there is nothing to record, which is also what a preview records.
func recordedFingerprint(records []edge.VariableRecord) string {
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

// liveVersionMarker stands where a version would be for a live key. It is not
// a number, so it can never collide with one.
const liveVersionMarker = "live"

// fingerprintValuesHexLen is how much of the digest an identity carries. 48
// bits, which only ever has to tell apart the handful of Deployments sharing
// one build id, and short enough that safeName's 40-character hard truncation
// cannot clip it out of a stack name.
const fingerprintValuesHexLen = 12

// fingerprintValues digests the resolved values a Deployment bakes, which is
// the half of its identity that a rotation changes. It is taken over the
// plaintext rather than the ciphertext: the data key is drawn fresh per render,
// so a ciphertext digest would differ on every deploy and no two deploys could
// ever be recognised as shipping the same values.
//
// Keys are sorted and each key and value written length-prefixed, so map order
// cannot move the digest and no re-splitting of one pair's characters into
// another can collide. Nothing baked fingerprints as empty, which renders the
// identity as the bare build id.
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
