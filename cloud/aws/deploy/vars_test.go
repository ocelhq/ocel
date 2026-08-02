package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/aws/vars/baked"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const (
	productionVarsKeyARN = "arn:aws:kms:us-east-1:1234:key/prod-key"
	previewVarsKeyARN    = "arn:aws:kms:us-east-1:1234:key/preview-key"
)

// TestVarsDecryptPolicy_GrantsOneKeyAndOneAction proves a function execution
// role can decrypt under its own substrate's class key and nothing else: a
// wildcard resource, or the other class's key, would hand preview compute
// production ciphertext.
func TestVarsDecryptPolicy_GrantsOneKeyAndOneAction(t *testing.T) {
	raw, err := varsReadPolicy(productionVarsKeyARN, "", "", "")
	if err != nil {
		t.Fatalf("varsReadPolicy: %v", err)
	}

	var doc struct {
		Statement []struct {
			Effect   string   `json:"Effect"`
			Action   []string `json:"Action"`
			Resource string   `json:"Resource"`
		}
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if len(doc.Statement) != 1 {
		t.Fatalf("got %d statements, want exactly the decrypt grant", len(doc.Statement))
	}
	st := doc.Statement[0]
	if st.Effect != "Allow" {
		t.Errorf("Effect = %q, want Allow", st.Effect)
	}
	if want := []string{"kms:Decrypt"}; !slices.Equal(st.Action, want) {
		t.Errorf("Action = %v, want %v", st.Action, want)
	}
	if st.Resource != productionVarsKeyARN {
		t.Errorf("Resource = %q, want the substrate's own key ARN", st.Resource)
	}
	if strings.Contains(raw, previewVarsKeyARN) {
		t.Errorf("policy = %s, must not reach another class's key", raw)
	}
}

// TestAppExecutionRole_CarriesTheSubstratesVarsKey proves the key a role
// decrypts under is the one the substrate this deploy resolved, not a name the
// deploy path derives: a production deploy can only ever render the production
// key into a role, because that is the only key it was handed.
func TestAppExecutionRole_CarriesTheSubstratesVarsKey(t *testing.T) {
	caches := map[string]*isrConfig{"web": {Prefix: "prod/proj/web/WEB1"}}

	role := appExecutionRole(Config{VarsKeyARN: productionVarsKeyARN}, "web", caches, appBundle{})
	if role.VarsKeyARN != productionVarsKeyARN {
		t.Errorf("VarsKeyARN = %q, want the substrate's own key", role.VarsKeyARN)
	}
	if role.Cache != caches["web"] {
		t.Errorf("Cache = %+v, want the app's own cache", role.Cache)
	}

	preview := appExecutionRole(Config{VarsKeyARN: previewVarsKeyARN}, "api", caches, appBundle{})
	if preview.VarsKeyARN != previewVarsKeyARN {
		t.Errorf("VarsKeyARN = %q, want the preview substrate's key", preview.VarsKeyARN)
	}
	if preview.Cache != nil {
		t.Errorf("Cache = %+v, want none for an app that keeps no cache", preview.Cache)
	}
}

func variable(key, value string, class resourcesv1.VariableClass) *deploymentsv1.ManifestVariable {
	return &deploymentsv1.ManifestVariable{Key: key, Class: class, Value: value}
}

// TestVariableEnv_OnlyPlainAndUnderItsBareKey proves the one property that
// distinguishes the plaintext class: the value lands under the name the user
// chose, so a library reading the process environment itself finds it. Every
// other class is delivered off the function's configuration entirely, so a
// value that is meant to be encrypted can never be read back from it.
func TestVariableEnv_OnlyPlainAndUnderItsBareKey(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name: "web",
		Variables: []*deploymentsv1.ManifestVariable{
			variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
			variable("WEBHOOK_SECRET", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		},
	}

	env := variableEnv(app)
	if got, want := env["POSTHOG_ID"], "ph-123"; got != want {
		t.Errorf("POSTHOG_ID = %q, want %q", got, want)
	}
	if len(env) != 1 {
		t.Errorf("env = %v, want the plaintext variable alone", env)
	}
	for _, raw := range env {
		if strings.Contains(raw, "sk-live") {
			t.Errorf("env = %v, must not carry an encrypted-class value", env)
		}
	}
}

// TestFunctionEnv_AccountsResourcesVariablesAndTheMembraneTogether proves the
// budget is charged against exactly what is deployed: a variable shares the
// function environment with the resource payloads and the membrane's own
// entries, so sizing any of them alone would let a deploy pass and then fail
// at AWS.
func TestFunctionEnv_AccountsResourcesVariablesAndTheMembraneTogether(t *testing.T) {
	base := map[string]string{
		"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"postgres://x"}`,
		"POSTHOG_ID":                  "ph-123",
	}

	env := functionEnv(base, functionArgs{Handler: "src/server.js"}, &isrConfig{Prefix: "prod/proj/web/B1"})

	for _, key := range []string{"OCEL_RESOURCE_POSTGRES_main", "POSTHOG_ID", "OCEL_HANDLER", "AWS_LAMBDA_EXEC_WRAPPER", "OCEL_ISR_PREFIX"} {
		if _, ok := env[key]; !ok {
			t.Errorf("env is missing %s; the budget would be charged against less than is deployed", key)
		}
	}
	if _, ok := base["OCEL_HANDLER"]; ok {
		t.Error("functionEnv mutated the shared base env")
	}
}

// TestCheckFunctionEnvBudget_UnderAtAndOverTheLimit proves the boundary is the
// platform's own: a set that fits is deployed, and one byte past it fails
// rather than being silently truncated by AWS.
func TestCheckFunctionEnvBudget_UnderAtAndOverTheLimit(t *testing.T) {
	sized := func(bytes int) map[string]string {
		key := "K"
		return map[string]string{key: strings.Repeat("v", bytes-len(key))}
	}

	if err := checkFunctionEnvBudget("web_index", sized(functionEnvBudgetBytes-1)); err != nil {
		t.Errorf("one byte under the limit: %v", err)
	}
	if err := checkFunctionEnvBudget("web_index", sized(functionEnvBudgetBytes)); err != nil {
		t.Errorf("exactly at the limit: %v", err)
	}
	if err := checkFunctionEnvBudget("web_index", sized(functionEnvBudgetBytes+1)); err == nil {
		t.Error("one byte over the limit was accepted; the deploy would fail at AWS instead")
	}
}

// TestCheckFunctionEnvBudget_NamesEveryKeysBytesAndTheRemedy proves the
// failure is actionable: the operator sees which key is spending the budget
// and what to do about it, rather than a bare size number.
func TestCheckFunctionEnvBudget_NamesEveryKeysBytesAndTheRemedy(t *testing.T) {
	env := map[string]string{
		"BIG_ONE":   strings.Repeat("a", 3000),
		"SMALL_ONE": strings.Repeat("b", 1200),
	}

	err := checkFunctionEnvBudget("web_index", env)
	if err == nil {
		t.Fatal("an over-budget environment was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"web_index", "BIG_ONE", "3007", "SMALL_ONE", "1209", "sensitive"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "secret") {
		t.Errorf("the remedy names a class that is not delivered yet, so following it loses the value:\n%s", msg)
	}
	if strings.Index(msg, "BIG_ONE") > strings.Index(msg, "SMALL_ONE") {
		t.Errorf("keys are not ordered by what they cost:\n%s", msg)
	}
}

// TestVariableEnv_CarriesTheAppsFolderBinding proves a bound app is told what
// it is bound to. Without it every app reports the project root, so the one
// case folders exist for — two apps, one key name, different values — is also
// the case whose out-of-scope error would name the wrong folder.
func TestVariableEnv_CarriesTheAppsFolderBinding(t *testing.T) {
	bound := variableEnv(&deploymentsv1.ManifestApp{Name: "admin", Folder: "/admin"})
	if got, want := bound["OCEL_APP_FOLDER"], "/admin"; got != want {
		t.Errorf("OCEL_APP_FOLDER = %q, want %q", got, want)
	}

	root := variableEnv(&deploymentsv1.ManifestApp{Name: "web"})
	if _, ok := root["OCEL_APP_FOLDER"]; ok {
		t.Errorf("env = %v, want no binding at all: the root is the absence of one", root)
	}
}

// TestRenderBakedBundle_EnvelopeIsTheDataKeyAndTheValuesAreOnlyCiphertext
// proves what the class actually delivers: the function configuration carries
// the raw per-deploy data key and nothing else, and the values exist only as
// ciphertext inside the bundle. The key being usable as-is is the point — the
// membrane opens the bundle with it alone, so init makes no external call.
func TestRenderBakedBundle_EnvelopeIsTheDataKeyAndTheValuesAreOnlyCiphertext(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name: "web",
		Variables: []*deploymentsv1.ManifestVariable{
			variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
			variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		},
	}

	bundle, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	if bytes.Contains(bundle.Ciphertext, []byte("sk-live")) {
		t.Error("the bundle carries a sensitive value in the clear")
	}
	env := bundle.env()
	if len(env) != 1 || env[baked.EnvelopeVar] == "" {
		t.Fatalf("configuration = %v, want the data key alone", env)
	}
	for _, value := range env {
		if strings.Contains(value, "sk-live") {
			t.Errorf("configuration = %v, discloses a sensitive value", env)
		}
	}

	key, err := base64.StdEncoding.DecodeString(env[baked.EnvelopeVar])
	if err != nil {
		t.Fatalf("envelope is not base64: %v", err)
	}
	if len(key) != baked.KeyBytes {
		t.Fatalf("envelope holds %d bytes, want the %d-byte data key itself", len(key), baked.KeyBytes)
	}
	values, err := baked.Open(key, bundle.Ciphertext)
	if err != nil {
		t.Fatalf("the bundle does not open under the key its envelope carries: %v", err)
	}
	if got, want := values["STRIPE_API_KEY"], "sk-live"; got != want {
		t.Errorf("STRIPE_API_KEY = %q, want %q", got, want)
	}
	if len(values) != 1 {
		t.Errorf("bundle = %v, want the encrypted-baked variable alone", values)
	}
}

// TestRenderBakedBundle_EveryRenderGetsAFreshDataKey proves the key is drawn
// per deploy rather than derived from anything the app carries: two renders of
// the identical app share neither key nor ciphertext, which is what keeps one
// deployment's artifact from opening another's.
func TestRenderBakedBundle_EveryRenderGetsAFreshDataKey(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name:      "web",
		Variables: []*deploymentsv1.ManifestVariable{variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)},
	}

	first, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	second, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}

	if first.Envelope == second.Envelope {
		t.Error("two renders share a data key; one deployment's key would open another's bundle")
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Error("two renders produce identical ciphertext; a rotation would reuse the old artifact")
	}
	other, err := base64.StdEncoding.DecodeString(second.Envelope)
	if err != nil {
		t.Fatalf("envelope is not base64: %v", err)
	}
	if _, err := baked.Open(other, first.Ciphertext); err == nil {
		t.Error("the second render's key opens the first render's bundle")
	}
}

// TestRenderBakedBundle_AnAppWithNoBakedValuesCostsNothing proves an app that
// declares none gains no configuration entry: the class is opt-in per app, and
// a data key with nothing behind it would still spend the function environment
// budget.
func TestRenderBakedBundle_AnAppWithNoBakedValuesCostsNothing(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name:      "web",
		Variables: []*deploymentsv1.ManifestVariable{variable("POSTHOG_ID", "ph", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)},
	}

	bundle, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	if len(bundle.Ciphertext) != 0 || len(bundle.env()) != 0 || len(bundle.overlay()) != 0 {
		t.Errorf("bundle = %+v, want nothing at all", bundle)
	}
}

// TestRenderBakedBundle_FingerprintTracksThePlaintextNotTheCiphertext is the
// executable form of the constraint the whole rotation path rests on. The data
// key is drawn fresh every render, so the ciphertext differs even when nothing
// changed; a fingerprint taken over it would mint a new Deployment identity on
// every deploy unconditionally. It has to be taken over the values.
func TestRenderBakedBundle_FingerprintTracksThePlaintextNotTheCiphertext(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name:      "web",
		Variables: []*deploymentsv1.ManifestVariable{variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)},
	}

	first, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	second, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatal("the two renders share ciphertext; this test can no longer tell the plaintext digest from a ciphertext one")
	}
	if first.Fingerprint == "" {
		t.Fatal("Fingerprint is empty for an app that bakes a value")
	}
	if first.Fingerprint != second.Fingerprint {
		t.Errorf("Fingerprint = %q then %q for unchanged values; every deploy would mint a new Deployment", first.Fingerprint, second.Fingerprint)
	}

	app.Variables = []*deploymentsv1.ManifestVariable{variable("STRIPE_API_KEY", "sk-live-2", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)}
	rotated, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	if rotated.Fingerprint == first.Fingerprint {
		t.Error("a rotated value renders the same Fingerprint; the rotation would collide with the Deployment it replaces")
	}
}

// TestRenderBakedBundle_NoBakedValuesHaveNoFingerprint keeps an app that bakes
// nothing on a bare-build-id identity, so its records and stack names are
// byte-for-byte what they were before fingerprints existed.
func TestRenderBakedBundle_NoBakedValuesHaveNoFingerprint(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name: "web",
		Variables: []*deploymentsv1.ManifestVariable{
			variable("POSTHOG_ID", "ph", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			variable("WEBHOOK_SECRET", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		},
	}

	bundle, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	if bundle.Fingerprint != "" {
		t.Errorf("Fingerprint = %q, want empty: nothing is baked", bundle.Fingerprint)
	}
}

// TestFingerprintValues_SameValuesSameDigestDifferentValuesDifferent proves
// what the identity rests on: the fingerprint is a function of the values
// themselves, so an unchanged set re-renders to the same Deployment identity
// and a rotated one to a different Deployment. The key/value boundary is part
// of what is digested, so no re-splitting of one key's characters into another
// can collide.
func TestFingerprintValues_SameValuesSameDigestDifferentValuesDifferent(t *testing.T) {
	values := map[string]string{"STRIPE_API_KEY": "sk-live", "SENTRY_DSN": "https://sentry"}

	first := fingerprintValues(values)
	if second := fingerprintValues(map[string]string{"SENTRY_DSN": "https://sentry", "STRIPE_API_KEY": "sk-live"}); first != second {
		t.Errorf("fingerprint = %q then %q, want the same values to fingerprint the same", first, second)
	}
	if rotated := fingerprintValues(map[string]string{"STRIPE_API_KEY": "sk-live-2", "SENTRY_DSN": "https://sentry"}); rotated == first {
		t.Error("a rotated value fingerprints the same; the rotation would reuse the prior Deployment identity")
	}
	if added := fingerprintValues(map[string]string{"STRIPE_API_KEY": "sk-live", "SENTRY_DSN": "https://sentry", "EXTRA": ""}); added == first {
		t.Error("an added key fingerprints the same")
	}
	if a, b := fingerprintValues(map[string]string{"AB": "C"}), fingerprintValues(map[string]string{"A": "BC"}); a == b {
		t.Errorf("{AB:C} and {A:BC} both fingerprint %q; the key boundary is not digested", a)
	}

	for name, empty := range map[string]map[string]string{"nil": nil, "empty": {}} {
		if got := fingerprintValues(empty); got != "" {
			t.Errorf("fingerprintValues(%s) = %q, want empty so the identity stays the bare build id", name, got)
		}
	}

	if strings.ContainsAny(first, "~ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Errorf("fingerprint %q is not lowercase hex; it must survive NewDeploymentIdentity and a stack name", first)
	}
}

// TestRenderBakedBundle_AClassWithNoDeliveryFailsTheDeploy proves the gap fails
// closed. A class this deploy path cannot deliver — `secret`, or an
// unrecognised one from a newer client — must stop the deploy naming the
// variable, because the alternative is a value that is simply absent at
// runtime and a deploy that reported success.
func TestRenderBakedBundle_AClassWithNoDeliveryFailsTheDeploy(t *testing.T) {
	cases := []struct {
		name  string
		class resourcesv1.VariableClass
	}{
		{"unspecified", resourcesv1.VariableClass_VARIABLE_CLASS_UNSPECIFIED},
		{"from a newer client", resourcesv1.VariableClass(99)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &deploymentsv1.ManifestApp{
				Name: "web",
				Variables: []*deploymentsv1.ManifestVariable{
					variable("POSTHOG_ID", "ph", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
					variable("WEBHOOK_SECRET", "whsec", tc.class),
				},
			}

			_, err := renderAppBundle(liveConfig(), "shop", app)
			if err == nil {
				t.Fatal("renderAppBundle = nil, want a class it cannot deliver refused")
			}
			for _, want := range []string{"web", "WEBHOOK_SECRET"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name %q: %v", want, err)
				}
			}
		})
	}
}

// TestRenderBakedBundles_SealsEachAppUnderItsOwnKey proves the render is
// per-app across a whole manifest: each app's bundle opens under its own
// envelope and under no other app's, and an app that bakes nothing contributes
// no entry at all.
func TestRenderBakedBundles_SealsEachAppUnderItsOwnKey(t *testing.T) {
	manifest := &deploymentsv1.Manifest{Apps: []*deploymentsv1.ManifestApp{
		{
			Name:      "web",
			Variables: []*deploymentsv1.ManifestVariable{variable("STRIPE_API_KEY", "sk-web", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)},
		},
		{
			Name:      "admin",
			Variables: []*deploymentsv1.ManifestVariable{variable("STRIPE_API_KEY", "sk-admin", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)},
		},
		{
			Name:      "docs",
			Variables: []*deploymentsv1.ManifestVariable{variable("POSTHOG_ID", "ph", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)},
		},
	}}

	bundles, err := renderAppBundles(liveConfig(), manifest)
	if err != nil {
		t.Fatalf("renderAppBundles: %v", err)
	}
	if len(bundles) != 2 {
		t.Fatalf("bundles = %v, want one for each app that bakes a value", bundles)
	}
	if _, ok := bundles["docs"]; ok {
		t.Error("an app that bakes nothing gained a bundle")
	}

	keys := make(map[string][]byte, len(bundles))
	for name, bundle := range bundles {
		key, err := base64.StdEncoding.DecodeString(bundle.Envelope)
		if err != nil {
			t.Fatalf("%s's envelope is not base64: %v", name, err)
		}
		keys[name] = key
	}
	for _, tc := range []struct{ app, want string }{{"web", "sk-web"}, {"admin", "sk-admin"}} {
		values, err := baked.Open(keys[tc.app], bundles[tc.app].Ciphertext)
		if err != nil {
			t.Fatalf("%s's bundle does not open under its own key: %v", tc.app, err)
		}
		if got := values["STRIPE_API_KEY"]; got != tc.want {
			t.Errorf("%s's STRIPE_API_KEY = %q, want %q", tc.app, got, tc.want)
		}
	}
	if _, err := baked.Open(keys["web"], bundles["admin"].Ciphertext); err == nil {
		t.Error("web's key opens admin's bundle; the apps do not have their own keys")
	}
}

// TestBakedBundle_OverlaysTheCiphertextAtTheAgreedPath proves the deploy
// writes the file where the membrane reads it. The two sides are held together
// by one constant, so a bundle can never ship values the runtime cannot find.
func TestBakedBundle_OverlaysTheCiphertextAtTheAgreedPath(t *testing.T) {
	bundle := appBundle{Envelope: "e", Ciphertext: []byte("sealed")}

	overlay := bundle.overlay()
	if len(overlay) != 1 {
		t.Fatalf("overlay = %v, want exactly the ciphertext file", overlay)
	}
	if got := overlay[baked.FilePath]; !bytes.Equal(got, []byte("sealed")) {
		t.Errorf("overlay[%q] = %q, want the sealed bytes", baked.FilePath, got)
	}
}

// TestBakedDelivery_CiphertextRidesInTheBundleAndNeverTheConfiguration is the
// property the class exists for, asserted over what a deploy actually produces:
// the plaintext appears nowhere in the uploaded artifact or in the function
// environment, the sealed file is inside the package at the path the membrane
// reads, and the only thing configuration gains is a wrapped data key.
func TestBakedDelivery_CiphertextRidesInTheBundleAndNeverTheConfiguration(t *testing.T) {
	dir := writeTree(t, map[string]string{"src/server.js": "handler"})
	manifest := &deploymentsv1.Manifest{
		Slug: "proj-1",
		Apps: []*deploymentsv1.ManifestApp{{
			Name: "web",
			Variables: []*deploymentsv1.ManifestVariable{
				variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
				variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			},
		}},
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", ArtifactPath: "web.func", App: "web"},
		},
	}
	uploader := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		ArtifactRoot:   filepath.Dir(dir),
		ArtifactBucket: "artifacts",
		Uploader:       uploader,
		VarsKeyARN:     productionVarsKeyARN,
	}
	manifest.Functions[0].ArtifactPath = filepath.Base(dir)

	bundles, err := renderAppBundles(liveConfig(), manifest)
	if err != nil {
		t.Fatalf("renderAppBundles: %v", err)
	}
	if _, err := uploadFunctionArtifacts(context.Background(), cfg, manifest, bundles, nil); err != nil {
		t.Fatalf("uploadFunctionArtifacts: %v", err)
	}

	if len(uploader.puts) != 1 {
		t.Fatalf("uploaded %d artifacts, want 1", len(uploader.puts))
	}
	packaged := readZip(t, []byte(uploader.putBodies[uploader.puts[0]]))
	if _, ok := packaged[baked.FilePath]; !ok {
		t.Fatalf("package = %v, want the sealed values at %s", packaged, baked.FilePath)
	}
	for path, contents := range packaged {
		if strings.Contains(contents, "sk-live") {
			t.Errorf("%s in the uploaded package carries the plaintext", path)
		}
	}

	env := variableEnv(manifest.GetApps()[0])
	for key, value := range bundles["web"].env() {
		env[key] = value
	}
	if _, ok := env["STRIPE_API_KEY"]; ok {
		t.Error("the function environment names an encrypted-baked variable")
	}
	for key, value := range env {
		if strings.Contains(value, "sk-live") {
			t.Errorf("env[%q] carries the plaintext", key)
		}
	}
	if env[baked.EnvelopeVar] == "" {
		t.Error("the function environment carries no data key; the membrane could not open the bundle")
	}
}
