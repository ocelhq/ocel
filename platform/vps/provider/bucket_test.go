package vps_test

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

const standingRootKey = "9a3b48264c5d6e7f8091a2b3c4d5e6f7"

func sealedRootKey() string {
	return base64.StdEncoding.EncodeToString([]byte(fakeSeal+
		base64.StdEncoding.EncodeToString([]byte(standingRootKey)))) + "\n"
}

func aBucket(t *testing.T, name string, public bool) resources.Instruction {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return resources.Instruction{
		Ref: providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack},
		Resource: providerkit.Resource{
			Name: name, Type: providerkit.BindingBucket,
			Bucket: &providerkit.BucketSpec{
				Public:         public,
				AllowedOrigins: []string{"https://app.example.com"},
			},
		},
	}
}

func TestAProviderOverABoxServesBuckets(t *testing.T) {
	t.Parallel()

	if served := over(&box{}).Serves(); !slices.Contains(served, providerkit.BindingBucket) {
		t.Errorf("Serves() = %v, and a project declaring a bucket is refused at deploy on a box", served)
	}
}

func TestADeclaredBucketStandsAStoreUpOnlyItsProjectReaches(t *testing.T) {
	t.Parallel()

	machine := &box{}
	binding, err := over(machine).Bucket(context.Background(), aBucket(t, "uploads", false), nil)
	if err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	if err := providerkit.VerifyProperties(binding); err != nil {
		t.Fatalf("the binding that came back is one no app can bind a client to: %v", err)
	}
	if binding.Name != "uploads" || binding.Type != providerkit.BindingBucket {
		t.Errorf("the binding is %s %q, want the bucket the project declared as uploads", binding.Type, binding.Name)
	}

	stood := machine.commands()[machine.at("'docker' 'run'")]
	for _, want := range []string{
		"'--network' 'ocel-production-shop'",
		"ocel.class=production",
		"ocel.project=shop",
		"'--cap-drop' 'ALL'",
		"@sha256:",
	} {
		if !strings.Contains(stood, want) {
			t.Errorf("the store was stood up without %q:\n%s", want, stood)
		}
	}
	for _, published := range []string{"--publish", "'-p'"} {
		if strings.Contains(stood, published) {
			t.Errorf("the store was stood up with %s, so the box's disk is reachable from the internet:\n%s", published, stood)
		}
	}
}

func TestOneStoreServesEveryBucketAProjectDeclares(t *testing.T) {
	t.Parallel()

	machine := &box{}
	provider := over(machine)
	first, err := provider.Bucket(context.Background(), aBucket(t, "uploads", false), nil)
	if err != nil {
		t.Fatalf("Bucket(uploads) = %v", err)
	}
	second, err := provider.Bucket(context.Background(), aBucket(t, "avatars", false), nil)
	if err != nil {
		t.Fatalf("Bucket(avatars) = %v", err)
	}

	if first.Properties[providerkit.PropertyBucket] == second.Properties[providerkit.PropertyBucket] {
		t.Fatalf("both buckets bind to %q, and two declared buckets are two stores of objects",
			first.Properties[providerkit.PropertyBucket])
	}
	stores := 0
	for _, command := range machine.commands() {
		if strings.Contains(command, "'docker' 'run'") && strings.Contains(command, "rustfs") {
			stores++
		}
	}
	if stores != 1 {
		t.Fatalf("two declared buckets stood %d stores up, and a box runs one per project and environment", stores)
	}
}

func TestTheStoreIsHeldToACredentialTheBoxKeepsSealed(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	if _, err := over(machine).Bucket(context.Background(), aBucket(t, "uploads", false), nil); err != nil {
		t.Fatalf("Bucket() = %v", err)
	}

	joined := strings.Join(machine.commands(), "\n")
	if !strings.Contains(joined, host.KeptPath(providerkit.ClassProduction, "prod-web-r0a1b2c3d-store-s3")) {
		t.Fatalf("nothing about the store's credential was kept on the box:\n%s", joined)
	}
	for _, fed := range machine.carried() {
		if strings.Contains(fed, standingRootKey) && !strings.Contains(fed, "RUSTFS_SECRET_KEY") {
			t.Fatalf("the store's root credential was carried to the box outside the env file it is handed in:\n%s", fed)
		}
	}
	handed := host.EnvFile(providerkit.ClassProduction, "prod-web-r0a1b2c3d-store-s3")
	if !strings.Contains(joined, "rm -f "+quotedPath(handed)) {
		t.Fatalf("the file the store's credential was handed over in is left standing on the box:\n%s", joined)
	}
}

func quotedPath(path string) string { return "'" + path + "'" }

func bindingBucket() providerkit.Binding {
	return providerkit.Binding{
		Type: providerkit.BindingBucket, Name: "uploads", Resource: "uploads",
		Properties: map[string]string{providerkit.PropertyBucket: "prod-web-r0a1b2c3d-uploads"},
	}
}

func storeManifest(t *testing.T, machine *box, options vps.Options) vars.Manifest {
	t.Helper()
	app := anApp()
	app.Values = providerkit.AppValues{Bindings: []providerkit.Binding{bindingBucket()}}
	provider := vps.ProviderOver(options, func(context.Context) (host.Conn, error) { return machine, nil })
	if _, err := provider.ProvisionContainers(context.Background(), aStack(t, app), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	for _, carried := range machine.carried() {
		for line := range strings.SplitSeq(carried, "\n") {
			raw, held := strings.CutPrefix(line, vars.EnvVar+"=")
			if !held {
				continue
			}
			parsed, err := vars.Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			return parsed
		}
	}
	t.Fatal("nothing the deploy carried to the box held a live manifest for the app")
	return vars.Manifest{}
}

func TestAnAppBindingABucketIsHandedItsStoreSealedAndNeverInPlaintext(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	manifest := storeManifest(t, machine, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}})
	if manifest.Store == nil {
		t.Fatal("the manifest names no store, so the runtime in front of the app has nothing to sign against")
	}
	if manifest.Store.Endpoint != "http://prod-web-r0a1b2c3d-store-s3:9000" || !manifest.Store.PathStyle {
		t.Errorf("the manifest points the runtime at %+v, want the store this project runs, addressed path-style", manifest.Store)
	}
	if manifest.Store.Sealed == "" {
		t.Error("the manifest carries no sealed credential, so the runtime could reach no store")
	}
	if strings.Contains(manifest.Store.Sealed, standingRootKey) {
		t.Errorf("the manifest carries the store credential in plaintext: %q", manifest.Store.Sealed)
	}
	if manifest.Store.Volume == "" {
		t.Error("the manifest names no volume, so the disk guard has nothing to measure")
	}
	if manifest.Store.Sessions == "" {
		t.Error("the manifest names no bucket for upload sessions to live in")
	}
}

func TestAnAppBindingNoBucketIsHandedNoStore(t *testing.T) {
	t.Parallel()

	machine := &box{}
	app := anApp()
	app.Values = providerkit.AppValues{Secrets: []providerkit.SecretRef{{Key: "DATABASE_URL"}}}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, app), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, `"store"`) {
			t.Errorf("an app binding no bucket was handed a store:\n%s", command)
		}
	}
}

func TestAnAppBoundToAnExternalStoreIsHandedThatStoreAndItsOwnPrefix(t *testing.T) {
	t.Parallel()

	machine := &box{}
	manifest := storeManifest(t, machine, vps.Options{
		SSH: vps.Target{Host: "box.invalid", User: "ada"},
		Bucket: vps.ExternalStore{
			Endpoint: "https://s3.example.com", Region: "eu-west-1", Bucket: "shared",
			AccessKeyID: "AKIA", SecretAccessKey: "elsewhere", PathStyle: true,
		},
	})
	if manifest.Store == nil || manifest.Store.Endpoint != "https://s3.example.com" {
		t.Fatalf("the manifest points the runtime at %+v, want the store the project was pointed at", manifest.Store)
	}
	if manifest.Store.Sessions != "shared/shop/prod" {
		t.Errorf("upload sessions live under %q, want the prefix this project and environment own inside the named bucket", manifest.Store.Sessions)
	}
	if manifest.Store.Volume != "" {
		t.Errorf("the manifest names volume %q for a store this box does not run", manifest.Store.Volume)
	}
	if strings.Contains(manifest.Store.Sealed, "elsewhere") {
		t.Errorf("the external store's credential rides the manifest in plaintext: %q", manifest.Store.Sealed)
	}
}

func TestRemovingABucketTakesItsObjectsWithIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	err := over(machine).RemoveResource(context.Background(),
		providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: aStackName(t)},
		providerkit.Binding{Type: providerkit.BindingBucket, Name: "uploads"}, nil)
	if err != nil {
		t.Fatalf("RemoveResource(bucket) = %v", err)
	}
	if len(machine.commands()) == 0 {
		t.Fatal("removing a bucket ran nothing on the box, so its objects stay on the disk forever")
	}
}

func aStackName(t *testing.T) naming.StackName {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return stack
}
