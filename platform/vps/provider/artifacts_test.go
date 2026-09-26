package vps_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func storeless(t *testing.T) *vps.Provider {
	t.Helper()
	return vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
}

func TestTheProviderSaysItKeepsNoArtifactStore(t *testing.T) {
	t.Parallel()

	if storeless(t).Facts().StoresArtifacts {
		t.Fatal("Facts().StoresArtifacts = true, but a container app puts nothing in the store")
	}
}

func TestTheArtifactPortRunsTheKitsPortTier(t *testing.T) {
	t.Parallel()

	p := storeless(t)
	conformance.RunArtifactStore(t, p.Facts(), p.Artifacts())
}

func TestAnUploadDrawsACreateRowAndThenFailsTheApplyLoudly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := storeless(t).Artifacts()
	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, []byte("a build artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Class:   edge.ClassProduction,
			Name:    naming.InfraStack("prod"),
		},
		Kind: provider.StackInfra,
		Uploads: []provider.Upload{{
			Name: "web",
			Ref:  provider.ArtifactRef{Class: edge.ClassProduction, Bucket: provider.StoreFunctions, Key: "shop/prod/web/bundle.zip"},
			Path: path,
		}},
	}

	drawn, err := resources.SynthesizedPlan(ctx, store, spec, provider.StackResult{})
	if err != nil {
		t.Fatalf("SynthesizedPlan() of a stack shipping one artifact = %v, want the row the human consents to", err)
	}
	rows := uploadRows(drawn)
	if len(rows) != 1 {
		t.Fatalf("the plan drew %d artifact rows, want 1: the row must precede the write even when the write is going to refuse", len(rows))
	}
	if rows[0].Action != provider.ActionCreate {
		t.Fatalf("the artifact row's action is %q, want %q: a store holding nothing has nothing to keep, and a keep row reads to the human as nothing to do", rows[0].Action, provider.ActionCreate)
	}

	var rejection refusal.Refusal
	err = resources.ShipUploads(ctx, store, spec.Uploads, nil)
	if !errors.As(err, &rejection) {
		t.Fatalf("ShipUploads() after the plan showed the row = %v, want a loud refusal rather than a write that vanishes", err)
	}
	if rejection.Code != refusal.CodeInvalid {
		t.Errorf("ShipUploads() refused with %q, want %q", rejection.Code, refusal.CodeInvalid)
	}
}

func uploadRows(plan provider.Plan) []provider.Change {
	var rows []provider.Change
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Kind == provider.UploadKind {
				rows = append(rows, change)
			}
		}
	}
	return rows
}
