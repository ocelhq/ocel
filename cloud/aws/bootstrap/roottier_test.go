package bootstrap

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestRootTierState_WriteThenReadRoundTrips(t *testing.T) {
	ssmc := newFakeSSM()
	state := edge.RootTierState{
		edge.RootTierKeyEndpoint:    "https://store.example",
		edge.RootTierKeyWriteSecret: "s3cr3t",
	}

	if err := WriteRootTierState(context.Background(), ssmc, "proj_1", state); err != nil {
		t.Fatalf("WriteRootTierState: %v", err)
	}

	got, err := ReadRootTierState(context.Background(), ssmc, "proj_1")
	if err != nil {
		t.Fatalf("ReadRootTierState: %v", err)
	}
	if got[edge.RootTierKeyEndpoint] != state[edge.RootTierKeyEndpoint] {
		t.Errorf("endpoint = %q, want %q", got[edge.RootTierKeyEndpoint], state[edge.RootTierKeyEndpoint])
	}
	if got[edge.RootTierKeyWriteSecret] != state[edge.RootTierKeyWriteSecret] {
		t.Errorf("write secret = %q, want %q", got[edge.RootTierKeyWriteSecret], state[edge.RootTierKeyWriteSecret])
	}
}

func TestRootTierState_ReadAbsentReturnsNilNotError(t *testing.T) {
	ssmc := newFakeSSM()

	got, err := ReadRootTierState(context.Background(), ssmc, "proj_never_deployed")
	if err != nil {
		t.Fatalf("ReadRootTierState on an absent parameter: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadRootTierState = %v, want nil/empty", got)
	}
}

func TestRootTierState_ScopedPerProject(t *testing.T) {
	ssmc := newFakeSSM()
	if err := WriteRootTierState(context.Background(), ssmc, "proj_a", edge.RootTierState{edge.RootTierKeyEndpoint: "https://a"}); err != nil {
		t.Fatalf("WriteRootTierState(proj_a): %v", err)
	}

	got, err := ReadRootTierState(context.Background(), ssmc, "proj_b")
	if err != nil {
		t.Fatalf("ReadRootTierState(proj_b): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("proj_b state = %v, want empty: state must not leak across projects", got)
	}
}

func TestRootTierState_OverwritesOnRewrite(t *testing.T) {
	ssmc := newFakeSSM()
	ctx := context.Background()
	if err := WriteRootTierState(ctx, ssmc, "proj_1", edge.RootTierState{edge.RootTierKeyEndpoint: "https://old"}); err != nil {
		t.Fatalf("first WriteRootTierState: %v", err)
	}
	if err := WriteRootTierState(ctx, ssmc, "proj_1", edge.RootTierState{edge.RootTierKeyEndpoint: "https://new"}); err != nil {
		t.Fatalf("second WriteRootTierState: %v", err)
	}

	got, err := ReadRootTierState(ctx, ssmc, "proj_1")
	if err != nil {
		t.Fatalf("ReadRootTierState: %v", err)
	}
	if got[edge.RootTierKeyEndpoint] != "https://new" {
		t.Errorf("endpoint = %q, want the overwritten value %q", got[edge.RootTierKeyEndpoint], "https://new")
	}
}
