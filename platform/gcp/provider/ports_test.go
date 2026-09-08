package gcp_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func standing(t *testing.T) *gcp.Provider {
	t.Helper()
	return gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"})
}

func TestEveryPortThisPhaseHasNotBuiltSaysSoRatherThanReadingAsDone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := standing(t)
	ref := providerkit.StackRef{Project: "acme", Class: providerkit.ClassProduction}

	bootstrapper, err := p.Bootstrap(p.Edges().Default())
	if err != nil {
		t.Fatalf("Bootstrap() = %v, want a bootstrapper whose own calls refuse", err)
	}

	for name, refused := range map[string]error{
		"Bootstrapper.Plan":        second(bootstrapper.Plan(ctx, providerkit.BootstrapRequest{})),
		"Bootstrapper.Apply":       bootstrapper.Apply(ctx, providerkit.BootstrapRequest{}, nil),
		"Bootstrapper.PlanRemoval": second(bootstrapper.PlanRemoval(ctx, providerkit.ClassProduction)),
		"Bootstrapper.Remove":      bootstrapper.Remove(ctx, providerkit.ClassProduction, nil),

		"Releaser.Plan":        second(p.Releases().Plan(ctx, providerkit.StackPlan{Ref: ref}, nil)),
		"Releaser.Provision":   second(p.Releases().Provision(ctx, providerkit.StackPlan{Ref: ref}, nil)),
		"Releaser.PlanDestroy": second(p.Releases().PlanDestroy(ctx, ref, nil)),
		"Releaser.Destroy":     p.Releases().Destroy(ctx, ref, nil),

		"ArtifactStore.Put":             p.Artifacts().Put(ctx, providerkit.ArtifactRef{}, bytes.NewReader(nil)),
		"ArtifactStore.Has":             second(p.Artifacts().Has(ctx, providerkit.ArtifactRef{})),
		"ArtifactStore.Open":            second(p.Artifacts().Open(ctx, providerkit.ArtifactRef{})),
		"ArtifactStore.RemovePrefix":    p.Artifacts().RemovePrefix(ctx, providerkit.ClassProduction, "", nil),
		"RecordStore.Write":             second(p.Records().Write(ctx, providerkit.Record{Name: providerkit.RecordName{"a"}})),
		"RecordStore.WritePair":         p.Records().WritePair(ctx, providerkit.Record{}, providerkit.Record{}),
		"RecordStore.Remove":            p.Records().Remove(ctx, providerkit.RecordName{"a"}, ""),
		"Sealer.Seal":                   second(p.Sealer().Seal(ctx, providerkit.Coordinate{}, nil)),
		"Sealer.Open":                   second(p.Sealer().Open(ctx, providerkit.Coordinate{}, nil)),
		"Credentials.Permissions":       second(p.Credentials().Permissions(providerkit.TierBootstrap)),
		"Credentials.PermissionsDeploy": second(p.Credentials().Permissions(providerkit.TierDeploy)),
	} {
		var refusal providerkit.Refusal
		if !errors.As(refused, &refusal) || refusal.Code != providerkit.CodeNotReady {
			t.Errorf("%s() = %v, want a %s refusal so a run stops here rather than continuing against nothing", name, refused, providerkit.CodeNotReady)
			continue
		}
		if !strings.HasPrefix(refusal.Message, "gcp: ") {
			t.Errorf("%s() refused with %q, want it to name the provider the refusal came from", name, refusal.Message)
		}
	}
}

func TestNoPortIsNilForTheKitToCallThrough(t *testing.T) {
	t.Parallel()

	p := standing(t)
	for name, port := range map[string]any{
		"Releases":    p.Releases(),
		"Artifacts":   p.Artifacts(),
		"Records":     p.Records(),
		"Sealer":      p.Sealer(),
		"Credentials": p.Credentials(),
		"Edges":       p.Edges(),
		"DNS":         p.DNS(),
	} {
		if port == nil {
			t.Errorf("%s() is nil, and the kit calls methods on it", name)
		}
	}
}

func TestTheRecordStoreReadsEmptyRatherThanRefusingWhatItHasNeverHeld(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := standing(t).Records()

	if _, err := records.Read(ctx, providerkit.RecordName{"projects"}); !errors.Is(err, providerkit.ErrNoRecord) {
		t.Errorf("Read() = %v, want ErrNoRecord: this provider has written nothing, so no name is taken", err)
	}
	held, err := records.List(ctx, providerkit.RecordName{"projects"})
	if err != nil || len(held) != 0 {
		t.Errorf("List() = %v, %v, want an empty listing", held, err)
	}
}

func TestServesNothingUntilAResourcePrimitiveExists(t *testing.T) {
	t.Parallel()

	p := standing(t)
	if got := p.Serves(); len(got) != 0 {
		t.Errorf("Serves() = %v, want nothing until this provider provisions links of its own", got)
	}
	want := []providerkit.Compute{providerkit.ComputeServerless, providerkit.ComputeContainer}
	got := p.Computes()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Computes() = %v, want %v with serverless first, which makes it the default", got, want)
	}
}

func second[T any](_ T, err error) error { return err }
