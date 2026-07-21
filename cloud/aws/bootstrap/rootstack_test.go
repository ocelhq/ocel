package bootstrap

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestRootStackState_WriteThenReadRoundTrips(t *testing.T) {
	ssmc := newFakeSSM()
	state := edge.RootStackState{
		edge.RootStackKeyEndpoint:    "https://store.example",
		edge.RootStackKeyWriteSecret: "s3cr3t",
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
	if got[edge.RootStackKeyWriteSecret] != state[edge.RootStackKeyWriteSecret] {
		t.Errorf("write secret = %q, want %q", got[edge.RootStackKeyWriteSecret], state[edge.RootStackKeyWriteSecret])
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
