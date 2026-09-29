package vps_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	s3store "github.com/ocelhq/ocel/platform/s3"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func TestAnAppReachesTheStoreUnderAnAccountOfItsOwn(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	manifest := storeManifest(t, machine, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}})

	own := host.StoreAccountKey("shop", manifest.Store.Env, "web")
	if manifest.Store.AccessKeyID != own {
		t.Errorf("the app reaches the store as %q, want the account %q this app alone owns: the root credential reaches every bucket on the box",
			manifest.Store.AccessKeyID, own)
	}

	granted := strings.Join(machine.commands(), "\n")
	if !strings.Contains(granted, "add-service-account") || !strings.Contains(granted, "update-service-account") {
		t.Fatalf("the store was never asked for an account of the app's own:\n%s", granted)
	}
}

func fedBy(t *testing.T, machine *box, needle string) string {
	t.Helper()
	encoded := machine.fedTo(needle)
	if encoded == "" {
		t.Fatalf("nothing was fed to the store by the call that would %s", needle)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("what is fed to the store is not what the box decodes: %v", err)
	}
	return string(decoded)
}

func TestAnAppsStoreAccountIsLimitedToTheBucketsItBinds(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	storeManifest(t, machine, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}})

	policy := fedBy(t, machine, "add-service-account")
	for what, named := range map[string]string{
		"the bucket the app binds":        "prod-web-r0a1b2c3d-uploads",
		"the store's own sessions":        s3store.SessionsBucket(),
		"a policy naming what it reaches": "arn:aws:s3:::",
	} {
		if !strings.Contains(policy, named) {
			t.Errorf("the account is granted without %s:\n%s", what, policy)
		}
	}
	if strings.Contains(policy, `"arn:aws:s3:::*"`) {
		t.Error("the app's account is granted every bucket on the store, which is the root credential by another name")
	}
}

func storeOfProject(t *testing.T, project string) *vars.Store {
	t.Helper()
	machine := &box{kept: sealedRootKey()}
	app := anApp()
	app.Values = provider.AppValues{Bindings: []provider.Binding{bindingBucket()}}
	spec := aStack(t, app)
	spec.Ref.Project = project
	if _, err := over(machine).ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	store := manifestIn(t, machine).Store
	if store == nil {
		t.Fatalf("app web of project %s binds a bucket and was handed no store", project)
	}
	return store
}

func TestRemovingOneProjectsAppNeverReachesAnotherProjectsStoreAccount(t *testing.T) {
	t.Parallel()

	shop := storeOfProject(t, "shop")
	machine := &box{kept: sealedRootKey()}
	blog := provider.StackRef{Project: "blog", Tier: environment.TierProduction, Name: aStackName(t)}
	err := over(machine).RemoveContainers(context.Background(), blog,
		[]provider.AppContainer{{Name: "web", Physical: "blog-prod-web-r0a1b2c3d-web"}}, nil)
	if err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}
	if joined := strings.Join(machine.commands(), "\n"); strings.Contains(joined, shop.AccessKeyID) {
		t.Errorf("project blog's app web went and the removal reached project shop's store account %s:\n%s", shop.AccessKeyID, joined)
	}
}
