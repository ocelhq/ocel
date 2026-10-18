package providerserver_test

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestDeployTellsTheStacksPortWhetherItsRecordsAlreadyNameEachStack(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{})
	client := servedBy(t, vendor)
	bootstrappedOverRPC(t, client)

	for range 2 {
		if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
			t.Fatalf("Deploy() = %q", result.GetError())
		}
	}
	specs := vendor.FakeStacks().Provisioned()
	infra := specs[0].Ref.Name
	var first, second *provider.StackSpec
	for at := range specs {
		if specs[at].Ref.Name != infra {
			continue
		}
		if first == nil {
			first = &specs[at]
		} else {
			second = &specs[at]
		}
	}
	if first == nil || second == nil {
		t.Fatalf("the two deploys provisioned %d specs, want the infra stack twice", len(specs))
	}
	if first.Recorded {
		t.Error("the first deploy says its records name the infra stack, so a vendor would deploy over a stack it finds but never made")
	}
	if !second.Recorded {
		t.Error("the second deploy says its records name no infra stack, so a vendor would refuse to deploy over what it provisioned itself")
	}
}

func connectMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type preflighting struct {
	*fake.Provider

	mu       sync.Mutex
	uploaded []string
}

func (p *preflighting) Hooks() provider.Hooks {
	hooks := p.Provider.Hooks()
	hooks.Preflight = &provider.PreflightHooks{Plan: p.PlanPreflight, Apply: p.ApplyPreflight}
	return hooks
}

func (p *preflighting) Artifacts() provider.ArtifactStore {
	return watchedArtifacts{ArtifactStore: p.Provider.Artifacts(), on: p}
}

func (p *preflighting) uploads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.uploaded)
}

type watchedArtifacts struct {
	provider.ArtifactStore
	on *preflighting
}

func (w watchedArtifacts) Put(ctx context.Context, ref provider.ArtifactRef, body io.Reader) error {
	w.on.mu.Lock()
	w.on.uploaded = append(w.on.uploaded, ref.Key)
	w.on.mu.Unlock()
	return w.ArtifactStore.Put(ctx, ref, body)
}

func TestDeployHandsPreflightTheSpecBeforeItUploadsAnything(t *testing.T) {
	builtProject(t)
	vendor := &preflighting{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, vendor)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	preflighted := vendor.Preflighted()
	if len(preflighted) != 1 {
		t.Fatalf("the deploy ran %d preflights, want the one that precedes the upload", len(preflighted))
	}
	pre := preflighted[0]
	if pre.Deploy.Slug != "shop" || len(pre.Deploy.Apps) != 1 {
		t.Errorf("preflight saw a plan for %q with %d apps, want the project and the app the manifest declares", pre.Deploy.Slug, len(pre.Deploy.Apps))
	}
	if len(pre.Resources) != 1 || pre.Resources[0].Name != "orders" {
		t.Errorf("preflight saw resources %+v, want the one the manifest declares", pre.Resources)
	}
	if pre.Progress == nil {
		t.Error("preflight was handed no reporter, and a vendor check has nothing to say through")
	}
}

func TestPreflightSeesWhichResourcesEachAppUses(t *testing.T) {
	builtProject(t)
	vendor := &preflighting{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, vendor)

	if result, _ := deploy(t, client, twoAppRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	used := map[string][]string{}
	for _, app := range vendor.Preflighted()[0].Apps {
		for _, resource := range app.Resources {
			used[app.App] = append(used[app.App], resource.Name)
		}
		slices.Sort(used[app.App])
	}
	if !slices.Equal(used["web"], []string{"orders"}) {
		t.Errorf("preflight saw web using %v, want only what its usage edges name", used["web"])
	}
	if !slices.Equal(used["admin"], []string{"orders", "uploads"}) {
		t.Errorf("preflight saw admin using %v, want both of the resources it declares usages for", used["admin"])
	}
}

func TestDeployRefusedByPreflightUploadsNothing(t *testing.T) {
	builtProject(t)
	vendor := &preflighting{Provider: fake.NewProvider(fake.Options{})}
	vendor.RefusePreflight(refusal.Refuse(refusal.CodeInvalid, "this account has no room for what the manifest asks for"))
	client := servedBy(t, vendor)

	stream, err := client.Deploy(context.Background(), rooted(deployRequest()))
	if err != nil {
		t.Fatal(err)
	}
	var failure string
	for stream.Receive() {
		if result := stream.Msg().GetResult(); result != nil {
			failure = result.GetError()
		}
	}
	said := failure + connectMessage(stream.Err())
	stream.Close()

	if said == "" {
		t.Fatal("Deploy() shipped past a refusing preflight, want it stopped")
	}
	if !strings.Contains(said, "no room") {
		t.Errorf("Deploy() failed with %q, want the preflight's own refusal", said)
	}
	if uploaded := vendor.uploads(); len(uploaded) != 0 {
		t.Errorf("the deploy uploaded %v after a refusing preflight, want nothing put in the store", uploaded)
	}
}
