package providerkit_test

import (
	"context"
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
