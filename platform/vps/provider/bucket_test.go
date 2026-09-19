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
	"github.com/ocelhq/ocel/platform/vps/provider/host"
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
