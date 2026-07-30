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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

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
	raw, err := varsDecryptPolicy(productionVarsKeyARN)
	if err != nil {
		t.Fatalf("varsDecryptPolicy: %v", err)
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

	role := appExecutionRole(Config{VarsKeyARN: productionVarsKeyARN}, "web", caches)
	if role.VarsKeyARN != productionVarsKeyARN {
		t.Errorf("VarsKeyARN = %q, want the substrate's own key", role.VarsKeyARN)
	}
	if role.Cache != caches["web"] {
		t.Errorf("Cache = %+v, want the app's own cache", role.Cache)
	}

	preview := appExecutionRole(Config{VarsKeyARN: previewVarsKeyARN}, "api", caches)
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
	for _, want := range []string{"web_index", "BIG_ONE", "3007", "SMALL_ONE", "1209", "sensitive", "secret"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
	if strings.Index(msg, "BIG_ONE") > strings.Index(msg, "SMALL_ONE") {
		t.Errorf("keys are not ordered by what they cost:\n%s", msg)
	}
}

// fakeDataKeyMaker stands in for KMS: it hands back a data key and a wrapped
// form of it that only this fake can unwrap, which is all the deploy side
// needs to be exercised without a key.
type fakeDataKeyMaker struct {
	keyID string
	spec  kmstypes.DataKeySpec
	calls int
	err   error
}

func (f *fakeDataKeyMaker) GenerateDataKey(_ context.Context, in *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	f.keyID = aws.ToString(in.KeyId)
	f.spec = in.KeySpec
	key := bytes.Repeat([]byte{7}, baked.KeyBytes)
	return &kms.GenerateDataKeyOutput{Plaintext: key, CiphertextBlob: append([]byte("wrapped:"), key...)}, nil
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

// TestRenderBakedBundle_SealsTheValuesAndDisclosesOnlyTheWrappedKey proves the
// whole point of the class: what reaches the function's configuration is a
// data key wrapped under the substrate's own key, and the values themselves
// exist only as ciphertext, which is what rides in the bundle.
func TestRenderBakedBundle_SealsTheValuesAndDisclosesOnlyTheWrappedKey(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name: "web",
		Variables: []*deploymentsv1.ManifestVariable{
			variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
			variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			variable("WEBHOOK_SECRET", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		},
	}
	km := &fakeDataKeyMaker{}

	bundle, err := renderBakedBundle(context.Background(), km, productionVarsKeyARN, app)
	if err != nil {
		t.Fatalf("renderBakedBundle: %v", err)
	}
	if km.keyID != productionVarsKeyARN {
		t.Errorf("data key generated under %q, want the substrate's own key", km.keyID)
	}
	if km.spec != kmstypes.DataKeySpecAes256 {
		t.Errorf("key spec = %v, want AES-256", km.spec)
	}

	if bytes.Contains(bundle.Ciphertext, []byte("sk-live")) {
		t.Error("the bundle carries a sensitive value in the clear")
	}
	env := bundle.env()
	if len(env) != 1 || env[baked.EnvelopeVar] == "" {
		t.Fatalf("configuration = %v, want the wrapped data key alone", env)
	}
	for _, value := range env {
		if strings.Contains(value, "sk-live") {
			t.Errorf("configuration = %v, discloses a sensitive value", env)
		}
	}

	wrapped, err := base64.StdEncoding.DecodeString(env[baked.EnvelopeVar])
	if err != nil {
		t.Fatalf("envelope is not base64: %v", err)
	}
	values, err := baked.Open(bytes.TrimPrefix(wrapped, []byte("wrapped:")), bundle.Ciphertext)
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

// TestRenderBakedBundle_AnAppWithNoBakedValuesCostsNothing proves an app that
// declares none neither reaches KMS nor gains a configuration entry: the class
// is opt-in per app, and a wrapped key with nothing behind it would still be
// spending the function environment budget.
func TestRenderBakedBundle_AnAppWithNoBakedValuesCostsNothing(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name:      "web",
		Variables: []*deploymentsv1.ManifestVariable{variable("POSTHOG_ID", "ph", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)},
	}
	km := &fakeDataKeyMaker{}

	bundle, err := renderBakedBundle(context.Background(), km, productionVarsKeyARN, app)
	if err != nil {
		t.Fatalf("renderBakedBundle: %v", err)
	}
	if km.calls != 0 {
		t.Errorf("KMS was called %d times for an app with no baked values", km.calls)
	}
	if len(bundle.Ciphertext) != 0 || len(bundle.env()) != 0 || len(bundle.overlay()) != 0 {
		t.Errorf("bundle = %+v, want nothing at all", bundle)
	}
}

// TestBakedBundle_OverlaysTheCiphertextAtTheAgreedPath proves the deploy
// writes the file where the membrane reads it. The two sides are held together
// by one constant, so a bundle can never ship values the runtime cannot find.
func TestBakedBundle_OverlaysTheCiphertextAtTheAgreedPath(t *testing.T) {
	bundle := bakedBundle{Envelope: "e", Ciphertext: []byte("sealed")}

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
		VarsKMS:        &fakeDataKeyMaker{},
	}
	manifest.Functions[0].ArtifactPath = filepath.Base(dir)

	bundles, err := renderBakedBundles(context.Background(), cfg, manifest)
	if err != nil {
		t.Fatalf("renderBakedBundles: %v", err)
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
		t.Error("the function environment carries no wrapped data key; the membrane could not open the bundle")
	}
}

// TestRenderBakedBundle_WithoutAKeyMakerFailsBeforeAnythingIsPackaged proves a
// deploy that cannot seal says so, rather than packaging an app whose values
// silently never arrive.
func TestRenderBakedBundle_WithoutAKeyMakerFailsBeforeAnythingIsPackaged(t *testing.T) {
	manifest := &deploymentsv1.Manifest{Apps: []*deploymentsv1.ManifestApp{{
		Name:      "web",
		Variables: []*deploymentsv1.ManifestVariable{variable("STRIPE_API_KEY", "sk", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)},
	}}}

	_, err := renderBakedBundles(context.Background(), Config{VarsKeyARN: productionVarsKeyARN}, manifest)
	if err == nil {
		t.Fatal("renderBakedBundles = nil, want a deploy without a key maker refused")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("error does not name the app: %v", err)
	}
}
