package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (b *box) forget() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ran, b.fed = nil, nil
}

func (b *box) proxied() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.proxyDoc
}

func holdingBuckets(t *testing.T, provider *vps.Provider, stack naming.StackName, named ...string) {
	t.Helper()
	records := fake.NewRecords()
	bindings := make([]providerkit.Binding, 0, len(named))
	for _, name := range named {
		held := bindingBucket()
		held.Name, held.Resource = name, name
		held.Properties = map[string]string{providerkit.PropertyBucket: "prod-web-r0a1b2c3d-" + name}
		bindings = append(bindings, held)
	}
	recorded := providerkit.Stack{Kind: providerkit.StackApp, App: "web", Bindings: bindings}
	if err := providerkit.WriteStack(context.Background(), records,
		providerkit.ClassProduction, "shop", stack, recorded); err != nil {
		t.Fatal(err)
	}
	provider.Recording(records)
}

func standingBucket(t *testing.T, machine *box, provider *vps.Provider, name string) providerkit.Binding {
	t.Helper()
	binding, err := provider.Bucket(context.Background(), aBucket(t, name, false), nil)
	if err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	machine.forget()
	return binding
}

func TestTheStoreGoesDownWithTheLastBucketTheProjectKeepsInIt(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	provider := over(machine)
	stack := aStackName(t)
	binding := standingBucket(t, machine, provider, "uploads")
	holdingBuckets(t, provider, stack, "uploads")

	ref := providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack}
	if err := provider.RemoveResource(context.Background(), ref, binding, nil); err != nil {
		t.Fatalf("RemoveResource(bucket) = %v", err)
	}

	joined := strings.Join(machine.commands(), "\n")
	store := vps.StoreName(ref)
	for _, want := range []string{
		"docker rm --force '" + store + "'",
		"docker volume rm",
		"/kept/" + store,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the last bucket went and %q never ran, so the store it lived in stands on with its data and its credential:\n%s", want, joined)
		}
	}
	if strings.Contains(machine.proxied(), store+":9000") {
		t.Errorf("the proxy still forwards to a store this box no longer runs:\n%s", machine.proxied())
	}
}

func TestAStoreStillHoldingABucketIsLeftStanding(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	provider := over(machine)
	stack := aStackName(t)
	binding := standingBucket(t, machine, provider, "uploads")
	holdingBuckets(t, provider, stack, "uploads", "assets")

	ref := providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack}
	if err := provider.RemoveResource(context.Background(), ref, binding, nil); err != nil {
		t.Fatalf("RemoveResource(bucket) = %v", err)
	}

	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, "docker rm --force '"+vps.StoreName(ref)+"'") {
		t.Errorf("one bucket of two went and the store went with it, taking the other bucket's objects down:\n%s", joined)
	}
}

func TestATeardownThatStoppedHalfwayIsRunAgainWithoutComplaint(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	provider := over(machine)
	stack := aStackName(t)
	binding := standingBucket(t, machine, provider, "uploads")
	holdingBuckets(t, provider, stack, "uploads")

	ref := providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack}
	for again := range 2 {
		if err := provider.RemoveResource(context.Background(), ref, binding, nil); err != nil {
			t.Fatalf("RemoveResource(bucket) run %d = %v", again+1, err)
		}
	}
}

func TestAnAppThatGoesTakesItsOwnStoreAccountWithIt(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	provider := over(machine)
	stack := aStackName(t)
	standingBucket(t, machine, provider, "uploads")
	holdingBuckets(t, provider, stack, "uploads")

	ref := providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack}
	err := provider.RemoveContainers(context.Background(), ref,
		[]providerkit.AppContainer{{Name: "web", Physical: "prod-web-r0a1b2c3d-web"}}, nil)
	if err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}

	joined := strings.Join(machine.commands(), "\n")
	own := host.StoreAccountKey(naming.InfraStack(ref.Name.Env).String(), "web")
	if !strings.Contains(joined, "delete-service-account") || !strings.Contains(joined, own) {
		t.Errorf("the app went and the account it reached the store under stayed, so a credential outlives what it was minted for:\n%s", joined)
	}
	if !strings.Contains(joined, "/kept/"+own) {
		t.Errorf("the app's sealed store credential was left on the box:\n%s", joined)
	}
	if other := host.StoreAccountKey(naming.InfraStack(ref.Name.Env).String(), "api"); strings.Contains(joined, other) {
		t.Errorf("removing one app reached for another app's account:\n%s", joined)
	}
	if strings.Contains(joined, "docker rm --force '"+vps.StoreName(ref)+"'") {
		t.Errorf("one app of a project went and the store every app shares went with it:\n%s", joined)
	}
}
