package providerkit_test

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

type occupied struct{ *fake.Provider }

func (occupied) Inspect(context.Context, providerkit.StackRef) (providerkit.StackState, error) {
	return providerkit.StackState{Present: true}, nil
}

func TestDeployRefusesToAdoptAStackItHasNoRecordOf(t *testing.T) {
	builtProject(t)
	client := servedBy(t, occupied{fake.NewProvider(fake.Options{})})

	stream, err := client.Deploy(context.Background(), deployRequest())
	if err != nil {
		t.Fatal(err)
	}
	var failure string
	for stream.Receive() {
		if result := stream.Msg().GetResult(); result != nil {
			failure = result.GetError()
		}
	}
	streamErr := stream.Err()
	stream.Close()

	if failure == "" && streamErr == nil {
		t.Fatal("Deploy() stood a project up over a stack it never recorded, want it refused")
	}
	said := failure + connectMessage(streamErr)
	if !strings.Contains(said, "already standing") {
		t.Errorf("Deploy() failed with %q, want it to say the stack was already standing", said)
	}
}

func connectMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type embedding struct {
	*fake.Provider

	mu       sync.Mutex
	embedded []string
}

func (e *embedding) EmbedCode(_ context.Context, function string, ref providerkit.ArtifactRef, _ providerkit.Reporter) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.embedded = append(e.embedded, function+" "+ref.Key)
	return nil
}

type warming struct {
	*fake.Provider

	mu     sync.Mutex
	warmed []string
}

func (w *warming) Warm(_ context.Context, targets []string, _ providerkit.Reporter) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warmed = append(w.warmed, targets...)
	return nil
}

func TestDeployWarmsEveryFunctionAProviderKnowsHowToWarm(t *testing.T) {
	builtProject(t)
	provider := &warming{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if len(provider.warmed) != 1 {
		t.Fatalf("the deploy warmed %v, want the function it stood up", provider.warmed)
	}
}

func TestDeployEmbedsTheBytecodeCacheOfEveryFunctionItShipped(t *testing.T) {
	builtProject(t)
	provider := &embedding{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if len(provider.embedded) != 1 {
		t.Fatalf("the deploy embedded %v, want the artifact of the one function it shipped", provider.embedded)
	}
	if !strings.Contains(provider.embedded[0], ".zip") {
		t.Errorf("the deploy embedded %q, want it to name the artifact the function runs from", provider.embedded[0])
	}
}

type preflighting struct {
	*fake.Provider

	mu       sync.Mutex
	uploaded []string
}

func (p *preflighting) PreflightDeploy(ctx context.Context, pre providerkit.DeployPreflight) error {
	return fake.DeployPreflighter{Provider: p.Provider}.PreflightDeploy(ctx, pre)
}

func (p *preflighting) Artifacts() providerkit.ArtifactStore {
	return watchedArtifacts{ArtifactStore: p.Provider.Artifacts(), on: p}
}

func (p *preflighting) uploads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.uploaded)
}

type watchedArtifacts struct {
	providerkit.ArtifactStore
	on *preflighting
}

func (w watchedArtifacts) Put(ctx context.Context, ref providerkit.ArtifactRef, body io.Reader) error {
	w.on.mu.Lock()
	w.on.uploaded = append(w.on.uploaded, ref.Key)
	w.on.mu.Unlock()
	return w.ArtifactStore.Put(ctx, ref, body)
}

func TestDeployHandsPreflightThePlanBeforeItUploadsAnything(t *testing.T) {
	builtProject(t)
	provider := &preflighting{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	preflighted := provider.Preflighted()
	if len(preflighted) != 1 {
		t.Fatalf("the deploy ran %d preflights, want the one that precedes the upload", len(preflighted))
	}
	pre := preflighted[0]
	if pre.Plan.Slug != "shop" || len(pre.Plan.Apps) != 1 {
		t.Errorf("preflight saw plan %+v, want the project and the app the manifest declares", pre.Plan)
	}
	if len(pre.Resources) != 1 || pre.Resources[0].Name != "orders" {
		t.Errorf("preflight saw resources %+v, want the one the manifest declares", pre.Resources)
	}
	if pre.Report == nil {
		t.Error("preflight was handed no reporter, and a vendor check has nothing to say through")
	}
}

func TestDeployRefusedByPreflightUploadsNothing(t *testing.T) {
	builtProject(t)
	provider := &preflighting{Provider: fake.NewProvider(fake.Options{})}
	provider.RefusePreflight(providerkit.Refuse(providerkit.CodeInvalid, "this account holds no room for what the manifest asks for"))
	client := servedBy(t, provider)

	stream, err := client.Deploy(context.Background(), deployRequest())
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
	if uploaded := provider.uploads(); len(uploaded) != 0 {
		t.Errorf("the deploy uploaded %v after a refusing preflight, want nothing put in the store", uploaded)
	}
}
