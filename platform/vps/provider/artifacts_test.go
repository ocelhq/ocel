package vps_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
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
	plan := providerkit.StackPlan{
		Ref: providerkit.StackRef{
			Project: "shop",
			Class:   providerkit.ClassProduction,
			Name:    naming.InfraStack("prod"),
		},
		Kind: providerkit.StackInfra,
		Uploads: []providerkit.Upload{{
			Name: "web",
			Ref:  providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: providerkit.StoreFunctions, Key: "shop/prod/web/bundle.zip"},
			Path: path,
		}},
	}

	drawn, err := providerkit.SynthesizedPlan(ctx, store, plan, providerkit.StackResult{})
	if err != nil {
		t.Fatalf("SynthesizedPlan() of a stack shipping one artifact = %v, want the row the human consents to", err)
	}
	rows := uploadRows(drawn)
	if len(rows) != 1 {
		t.Fatalf("the plan drew %d artifact rows, want 1: the row must precede the write even when the write is going to refuse", len(rows))
	}
	if rows[0].Action != providerkit.ActionCreate {
		t.Fatalf("the artifact row's action is %q, want %q: a store holding nothing has nothing to keep, and a keep row reads to the human as nothing to do", rows[0].Action, providerkit.ActionCreate)
	}

	var refusal providerkit.Refusal
	err = providerkit.ShipUploads(ctx, store, plan.Uploads, nil)
	if !errors.As(err, &refusal) {
		t.Fatalf("ShipUploads() after the plan showed the row = %v, want a loud refusal rather than a write that vanishes", err)
	}
	if refusal.Code != providerkit.CodeInvalid {
		t.Errorf("ShipUploads() refused with %q, want %q", refusal.Code, providerkit.CodeInvalid)
	}
}

func uploadRows(plan providerkit.Plan) []providerkit.Change {
	var rows []providerkit.Change
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Kind == providerkit.UploadKind {
				rows = append(rows, change)
			}
		}
	}
	return rows
}
