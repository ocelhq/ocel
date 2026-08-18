package bootstrap

import (
	"context"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestStackState(t *testing.T) {
	t.Run("write then read round trips", func(t *testing.T) {
		ssmc := newFakeSSM()
		state := edge.StackState{
			edge.StackKeyEndpoint: "https://store.example",
			edge.StackKeySecret:   "s3cr3t",
		}

		if err := WriteStackStateFor(context.Background(), ssmc, ClassProduction, "proj-1", state); err != nil {
			t.Fatalf("WriteStackStateFor: %v", err)
		}

		got, err := ReadStackState(context.Background(), ssmc, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackState: %v", err)
		}
		if got[edge.StackKeyEndpoint] != state[edge.StackKeyEndpoint] {
			t.Errorf("endpoint = %q, want %q", got[edge.StackKeyEndpoint], state[edge.StackKeyEndpoint])
		}
		if got[edge.StackKeySecret] != state[edge.StackKeySecret] {
			t.Errorf("secret = %q, want %q", got[edge.StackKeySecret], state[edge.StackKeySecret])
		}
	})

	t.Run("read absent returns nil not error", func(t *testing.T) {
		ssmc := newFakeSSM()

		got, err := ReadStackState(context.Background(), ssmc, "proj-never-deployed")
		if err != nil {
			t.Fatalf("ReadStackState on an absent parameter: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("ReadStackState = %v, want nil/empty", got)
		}
	})

	t.Run("scoped per project", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := WriteStackStateFor(context.Background(), ssmc, ClassProduction, "proj-a", edge.StackState{edge.StackKeyEndpoint: "https://a"}); err != nil {
			t.Fatalf("WriteStackStateFor(proj-a): %v", err)
		}

		got, err := ReadStackState(context.Background(), ssmc, "proj-b")
		if err != nil {
			t.Fatalf("ReadStackState(proj-b): %v", err)
		}
		if len(got) != 0 {
			t.Errorf("proj-b state = %v, want empty: state must not leak across projects", got)
		}
	})

	t.Run("production and preview are separate", func(t *testing.T) {
		ssmc := newFakeSSM()
		ctx := context.Background()
		if err := WriteStackStateFor(ctx, ssmc, ClassProduction, "proj-1", edge.StackState{edge.StackKeySecret: "prod-secret"}); err != nil {
			t.Fatalf("WriteStackStateFor(production): %v", err)
		}
		if err := WriteStackStateFor(ctx, ssmc, ClassPreview, "proj-1", edge.StackState{edge.StackKeySecret: "preview-secret"}); err != nil {
			t.Fatalf("WriteStackStateFor(preview): %v", err)
		}

		prod, err := ReadStackStateFor(ctx, ssmc, ClassProduction, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackStateFor(production): %v", err)
		}
		preview, err := ReadStackStateFor(ctx, ssmc, ClassPreview, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackStateFor(preview): %v", err)
		}
		if prod[edge.StackKeySecret] != "prod-secret" {
			t.Errorf("production secret = %q, want prod-secret", prod[edge.StackKeySecret])
		}
		if preview[edge.StackKeySecret] != "preview-secret" {
			t.Errorf("preview secret = %q, want preview-secret: the two substrates must not share state", preview[edge.StackKeySecret])
		}
		if StackStateParamPrefix == PreviewStackStateParamPrefix {
			t.Error("production and preview root-stack state prefixes must differ")
		}

		if err := DeleteStackStateFor(ctx, ssmc, ClassPreview, "proj-1"); err != nil {
			t.Fatalf("DeleteStackStateFor(preview): %v", err)
		}
		stillProd, err := ReadStackStateFor(ctx, ssmc, ClassProduction, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackStateFor(production) after preview delete: %v", err)
		}
		if stillProd[edge.StackKeySecret] != "prod-secret" {
			t.Errorf("production state was disturbed by a preview delete: %v", stillProd)
		}
	})

	t.Run("overwrites on rewrite", func(t *testing.T) {
		ssmc := newFakeSSM()
		ctx := context.Background()
		if err := WriteStackStateFor(ctx, ssmc, ClassProduction, "proj-1", edge.StackState{edge.StackKeyEndpoint: "https://old"}); err != nil {
			t.Fatalf("first WriteStackStateFor: %v", err)
		}
		if err := WriteStackStateFor(ctx, ssmc, ClassProduction, "proj-1", edge.StackState{edge.StackKeyEndpoint: "https://new"}); err != nil {
			t.Fatalf("second WriteStackStateFor: %v", err)
		}

		got, err := ReadStackState(ctx, ssmc, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackState: %v", err)
		}
		if got[edge.StackKeyEndpoint] != "https://new" {
			t.Errorf("endpoint = %q, want the overwritten value %q", got[edge.StackKeyEndpoint], "https://new")
		}
	})
}

func TestDeleteStackState(t *testing.T) {
	t.Run("removes then reads absent", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := WriteStackStateFor(context.Background(), ssmc, ClassProduction, "proj-1", edge.StackState{edge.StackKeyEndpoint: "https://store"}); err != nil {
			t.Fatalf("WriteStackStateFor: %v", err)
		}

		if err := DeleteStackState(context.Background(), ssmc, "proj-1"); err != nil {
			t.Fatalf("DeleteStackState: %v", err)
		}

		got, err := ReadStackState(context.Background(), ssmc, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackState after delete: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("state after delete = %v, want empty", got)
		}
	})

	t.Run("absent is idempotent success", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := DeleteStackState(context.Background(), ssmc, "proj-never-deployed"); err != nil {
			t.Fatalf("DeleteStackState on an absent parameter: %v, want nil (idempotent)", err)
		}
	})
}

func TestStackStateFor(t *testing.T) {
	t.Run("unknown class errors", func(t *testing.T) {
		if _, err := ReadStackStateFor(context.Background(), newFakeSSM(), "nonsense", "proj-1"); err == nil {
			t.Error("ReadStackStateFor(unknown class) = nil error, want an error")
		}
	})
}
