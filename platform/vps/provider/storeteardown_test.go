package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func manifestAfterBucket(t *testing.T, machine *box) vars.Manifest {
	t.Helper()
	p := over(machine)
	binding, err := p.ProvisionBucket(context.Background(), aBucket(t, "uploads", false), nil)
	if err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	app := anApp()
	app.Values = provider.AppValues{Bindings: []provider.Binding{binding}}
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, app), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	return manifestIn(t, machine)
}

func lifecycleAnswering(said string) func(string) (session.Result, bool) {
	return func(command string) (session.Result, bool) {
		if !strings.Contains(command, "?lifecycle") {
			return session.Result{}, false
		}
		return session.Result{Stdout: said}, true
	}
}

func TestAStoreThatExpiresItsOwnUploadsIsNotSweptByTheRuntime(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey(), refuses: lifecycleAnswering("lifecycle=200\n")}
	if manifest := manifestAfterBucket(t, machine); manifest.Store == nil || manifest.Store.SweepUploads {
		t.Error("a store that took the rule to abandon unfinished uploads is swept by every app anyway, and that is a listing charged to writes for nothing")
	}
}

func TestAStoreThatRefusedToExpireItsOwnUploadsIsSweptByTheRuntime(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey(), refuses: lifecycleAnswering("lifecycle=501\n")}
	manifest := manifestAfterBucket(t, machine)
	if manifest.Store == nil || !manifest.Store.SweepUploads {
		t.Error("the store refused the rule that abandons unfinished uploads and nothing sweeps them, so an upload left open holds its parts on the volume forever")
	}
}

func TestAStoreThatRefusedToExpireItsUploadsIsSweptByADeployThatOnlyBuildsTheAppSection(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey(), refuses: lifecycleAnswering("lifecycle=501\n")}
	binding, err := over(machine).ProvisionBucket(context.Background(), aBucket(t, "uploads", false), nil)
	if err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	machine.forget()

	app := anApp()
	app.Values = provider.AppValues{Bindings: []provider.Binding{binding}}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, app), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	manifest := manifestIn(t, machine)
	if manifest.Store == nil || !manifest.Store.SweepUploads {
		t.Error("the store refused the rule that abandons unfinished uploads and the app was told to sweep nothing, because what the store answered was only ever held in the memory of the run that asked it")
	}
}

func (b *box) forget() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ran, b.fed = nil, nil
}

func (b *box) proxied() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.routingDoc
}

func holdingBuckets(t *testing.T, p *vps.Provider, stack naming.StackName, named ...string) {
	t.Helper()
	records := fake.NewRecords()
	bindings := make([]provider.Binding, 0, len(named))
	for _, name := range named {
		held := bindingBucket()
		held.Name, held.Resource = name, name
		held.Properties = map[string]string{provider.PropertyBucket: "prod-web-r0a1b2c3d-" + name}
		bindings = append(bindings, held)
	}
	recorded := providerkit.RecordedStack{Kind: provider.StackApp, App: "web", Bindings: bindings}
	if err := providerkit.WriteStack(context.Background(), records,
		edge.ClassProduction, "shop", stack, recorded); err != nil {
		t.Fatal(err)
	}
	p.Recording(records)
}

func standingBucket(t *testing.T, machine *box, p *vps.Provider, name string) provider.Binding {
	t.Helper()
	binding, err := p.ProvisionBucket(context.Background(), aBucket(t, name, false), nil)
	if err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	machine.forget()
	return binding
}

func TestTheStoreGoesDownWithTheLastBucketTheProjectKeepsInIt(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	p := over(machine)
	stack := aStackName(t)
	binding := standingBucket(t, machine, p, "uploads")
	holdingBuckets(t, p, stack, "uploads")

	ref := provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack}
	if err := p.RemoveResource(context.Background(), ref, binding, nil); err != nil {
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
	p := over(machine)
	stack := aStackName(t)
	binding := standingBucket(t, machine, p, "uploads")
	holdingBuckets(t, p, stack, "uploads", "assets")

	ref := provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack}
	if err := p.RemoveResource(context.Background(), ref, binding, nil); err != nil {
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
	p := over(machine)
	stack := aStackName(t)
	binding := standingBucket(t, machine, p, "uploads")
	holdingBuckets(t, p, stack, "uploads")

	ref := provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack}
	for again := range 2 {
		if err := p.RemoveResource(context.Background(), ref, binding, nil); err != nil {
			t.Fatalf("RemoveResource(bucket) run %d = %v", again+1, err)
		}
	}
}

func TestATeardownThatStoppedAfterTheStoreWentIsFinishedByTheNextRun(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	stack := aStackName(t)
	ref := provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack}
	own := host.StoreAccountKey(naming.InfraStack(ref.Name.Env).String(), "web")

	stopped := over(machine)
	binding := standingBucket(t, machine, stopped, "uploads")
	holdingBuckets(t, stopped, stack, "uploads")
	machine.refuses = func(command string) (session.Result, bool) {
		if !strings.Contains(command, "/kept/"+own) {
			return session.Result{}, false
		}
		return session.Result{Code: 1, Stderr: "the box went away"}, true
	}
	if err := stopped.RemoveResource(context.Background(), ref, binding, nil); err == nil {
		t.Fatal("RemoveResource(bucket) = nil, want the failure that stops the teardown halfway")
	}

	machine.refuses = nil
	machine.kept = ""
	machine.forget()
	again := over(machine)
	holdingBuckets(t, again, stack, "uploads")
	if err := again.RemoveResource(context.Background(), ref, binding, nil); err != nil {
		t.Fatalf("RemoveResource(bucket) run 2 = %v", err)
	}

	joined := strings.Join(machine.commands(), "\n")
	store := vps.StoreName(ref)
	for _, want := range []string{"docker rm --force '" + store + "'", "/kept/" + own} {
		if !strings.Contains(joined, want) {
			t.Errorf("a teardown that stopped after the store's credential went is never finished, and %q never ran on the run after it:\n%s", want, joined)
		}
	}
	if strings.Contains(machine.proxied(), store+":9000") {
		t.Errorf("the proxy still forwards to a store this box no longer runs:\n%s", machine.proxied())
	}
}

func TestAnAppThatGoesTakesItsOwnStoreAccountWithIt(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	p := over(machine)
	stack := aStackName(t)
	standingBucket(t, machine, p, "uploads")
	holdingBuckets(t, p, stack, "uploads")

	ref := provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack}
	err := p.RemoveContainers(context.Background(), ref,
		[]provider.AppContainer{{Name: "web", Physical: "prod-web-r0a1b2c3d-web"}}, nil)
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
