package bootstrap

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestRootStackState_WriteThenReadRoundTrips(t *testing.T) {
	ssmc := newFakeSSM()
	state := edge.RootStackState{
		edge.RootStackKeyEndpoint: "https://store.example",
		edge.RootStackKeySecret:   "s3cr3t",
	}

	if err := WriteRootStackState(context.Background(), ssmc, "proj_1", state); err != nil {
		t.Fatalf("WriteRootStackState: %v", err)
	}

	got, err := ReadRootStackState(context.Background(), ssmc, "proj_1")
	if err != nil {
		t.Fatalf("ReadRootStackState: %v", err)
	}
	if got[edge.RootStackKeyEndpoint] != state[edge.RootStackKeyEndpoint] {
		t.Errorf("endpoint = %q, want %q", got[edge.RootStackKeyEndpoint], state[edge.RootStackKeyEndpoint])
	}
	if got[edge.RootStackKeySecret] != state[edge.RootStackKeySecret] {
		t.Errorf("secret = %q, want %q", got[edge.RootStackKeySecret], state[edge.RootStackKeySecret])
	}
}

func TestRootStackState_ReadAbsentReturnsNilNotError(t *testing.T) {
	ssmc := newFakeSSM()

	got, err := ReadRootStackState(context.Background(), ssmc, "proj_never_deployed")
	if err != nil {
		t.Fatalf("ReadRootStackState on an absent parameter: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadRootStackState = %v, want nil/empty", got)
	}
}

func TestDeleteRootStackState_RemovesThenReadsAbsent(t *testing.T) {
	ssmc := newFakeSSM()
	if err := WriteRootStackState(context.Background(), ssmc, "proj_1", edge.RootStackState{edge.RootStackKeyEndpoint: "https://store"}); err != nil {
		t.Fatalf("WriteRootStackState: %v", err)
	}

	if err := DeleteRootStackState(context.Background(), ssmc, "proj_1"); err != nil {
		t.Fatalf("DeleteRootStackState: %v", err)
	}

	got, err := ReadRootStackState(context.Background(), ssmc, "proj_1")
	if err != nil {
		t.Fatalf("ReadRootStackState after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("state after delete = %v, want empty", got)
	}
}

func TestDeleteRootStackState_AbsentIsIdempotentSuccess(t *testing.T) {
	ssmc := newFakeSSM()
	if err := DeleteRootStackState(context.Background(), ssmc, "proj_never_deployed"); err != nil {
		t.Fatalf("DeleteRootStackState on an absent parameter: %v, want nil (idempotent)", err)
	}
}

func TestRootStackState_ScopedPerProject(t *testing.T) {
	ssmc := newFakeSSM()
	if err := WriteRootStackState(context.Background(), ssmc, "proj_a", edge.RootStackState{edge.RootStackKeyEndpoint: "https://a"}); err != nil {
		t.Fatalf("WriteRootStackState(proj_a): %v", err)
	}

	got, err := ReadRootStackState(context.Background(), ssmc, "proj_b")
	if err != nil {
		t.Fatalf("ReadRootStackState(proj_b): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("proj_b state = %v, want empty: state must not leak across projects", got)
	}
}

func TestRootStackState_ProductionAndPreviewAreSeparate(t *testing.T) {
	ssmc := newFakeSSM()
	ctx := context.Background()
	if err := WriteRootStackStateFor(ctx, ssmc, ClassProduction, "proj_1", edge.RootStackState{edge.RootStackKeySecret: "prod-secret"}); err != nil {
		t.Fatalf("WriteRootStackStateFor(production): %v", err)
	}
	if err := WriteRootStackStateFor(ctx, ssmc, ClassPreview, "proj_1", edge.RootStackState{edge.RootStackKeySecret: "preview-secret"}); err != nil {
		t.Fatalf("WriteRootStackStateFor(preview): %v", err)
	}

	prod, err := ReadRootStackStateFor(ctx, ssmc, ClassProduction, "proj_1")
	if err != nil {
		t.Fatalf("ReadRootStackStateFor(production): %v", err)
	}
	preview, err := ReadRootStackStateFor(ctx, ssmc, ClassPreview, "proj_1")
	if err != nil {
		t.Fatalf("ReadRootStackStateFor(preview): %v", err)
	}
	if prod[edge.RootStackKeySecret] != "prod-secret" {
		t.Errorf("production secret = %q, want prod-secret", prod[edge.RootStackKeySecret])
	}
	if preview[edge.RootStackKeySecret] != "preview-secret" {
		t.Errorf("preview secret = %q, want preview-secret: the two substrates must not share state", preview[edge.RootStackKeySecret])
	}
	if RootStackStateParamPrefix == PreviewRootStackStateParamPrefix {
		t.Error("production and preview root-stack state prefixes must differ")
	}

	// Deleting one substrate's state leaves the other intact.
	if err := DeleteRootStackStateFor(ctx, ssmc, ClassPreview, "proj_1"); err != nil {
		t.Fatalf("DeleteRootStackStateFor(preview): %v", err)
	}
	stillProd, err := ReadRootStackStateFor(ctx, ssmc, ClassProduction, "proj_1")
	if err != nil {
		t.Fatalf("ReadRootStackStateFor(production) after preview delete: %v", err)
	}
	if stillProd[edge.RootStackKeySecret] != "prod-secret" {
		t.Errorf("production state was disturbed by a preview delete: %v", stillProd)
	}
}

func TestRootStackStateFor_UnknownClassErrors(t *testing.T) {
	if _, err := ReadRootStackStateFor(context.Background(), newFakeSSM(), "nonsense", "proj_1"); err == nil {
		t.Error("ReadRootStackStateFor(unknown class) = nil error, want an error")
	}
}

func TestRootStackState_OverwritesOnRewrite(t *testing.T) {
	ssmc := newFakeSSM()
	ctx := context.Background()
	if err := WriteRootStackState(ctx, ssmc, "proj_1", edge.RootStackState{edge.RootStackKeyEndpoint: "https://old"}); err != nil {
		t.Fatalf("first WriteRootStackState: %v", err)
	}
	if err := WriteRootStackState(ctx, ssmc, "proj_1", edge.RootStackState{edge.RootStackKeyEndpoint: "https://new"}); err != nil {
		t.Fatalf("second WriteRootStackState: %v", err)
	}

	got, err := ReadRootStackState(ctx, ssmc, "proj_1")
	if err != nil {
		t.Fatalf("ReadRootStackState: %v", err)
	}
	if got[edge.RootStackKeyEndpoint] != "https://new" {
		t.Errorf("endpoint = %q, want the overwritten value %q", got[edge.RootStackKeyEndpoint], "https://new")
	}
}
