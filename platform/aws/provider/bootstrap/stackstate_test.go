package bootstrap

import (
	"context"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestStackState(t *testing.T) {
	t.Run("write then read round trips", func(t *testing.T) {
		ssmc := newFakeSSM()
		record := StackRecord{Edge: edge.StackState{Endpoint: "https://store.example", Secret: "s3cr3t"}}

		if err := WriteStackRecordFor(context.Background(), ssmc, ClassProduction, "proj-1", record); err != nil {
			t.Fatalf("WriteStackRecordFor: %v", err)
		}

		got, err := ReadStackRecord(context.Background(), ssmc, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackRecord: %v", err)
		}
		if !got.Edge.Equal(record.Edge) {
			t.Errorf("state = %+v, want %+v", got.Edge, record.Edge)
		}
	})

	t.Run("read absent returns nil not error", func(t *testing.T) {
		ssmc := newFakeSSM()

		got, err := ReadStackRecord(context.Background(), ssmc, "proj-never-deployed")
		if err != nil {
			t.Fatalf("ReadStackRecord on an absent parameter: %v", err)
		}
		if !got.Empty() {
			t.Errorf("ReadStackRecord = %+v, want nothing recorded", got)
		}
	})

	t.Run("scoped per project", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := WriteStackRecordFor(context.Background(), ssmc, ClassProduction, "proj-a", StackRecord{Edge: edge.StackState{Endpoint: "https://a"}}); err != nil {
			t.Fatalf("WriteStackRecordFor(proj-a): %v", err)
		}

		got, err := ReadStackRecord(context.Background(), ssmc, "proj-b")
		if err != nil {
			t.Fatalf("ReadStackRecord(proj-b): %v", err)
		}
		if !got.Empty() {
			t.Errorf("proj-b state = %+v, want empty: state must not leak across projects", got)
		}
	})

	t.Run("production and preview are separate", func(t *testing.T) {
		ssmc := newFakeSSM()
		ctx := context.Background()
		if err := WriteStackRecordFor(ctx, ssmc, ClassProduction, "proj-1", StackRecord{Edge: edge.StackState{Secret: "prod-secret"}}); err != nil {
			t.Fatalf("WriteStackRecordFor(production): %v", err)
		}
		if err := WriteStackRecordFor(ctx, ssmc, ClassPreview, "proj-1", StackRecord{Edge: edge.StackState{Secret: "preview-secret"}}); err != nil {
			t.Fatalf("WriteStackRecordFor(preview): %v", err)
		}

		prod, err := ReadStackRecordFor(ctx, ssmc, ClassProduction, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackRecordFor(production): %v", err)
		}
		preview, err := ReadStackRecordFor(ctx, ssmc, ClassPreview, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackRecordFor(preview): %v", err)
		}
		if prod.Edge.Secret != "prod-secret" {
			t.Errorf("production secret = %q, want prod-secret", prod.Edge.Secret)
		}
		if preview.Edge.Secret != "preview-secret" {
			t.Errorf("preview secret = %q, want preview-secret: the two bootstraps must not share state", preview.Edge.Secret)
		}
		if StackStateParamPrefix == PreviewStackStateParamPrefix {
			t.Error("production and preview root-stack state prefixes must differ")
		}

		if err := DeleteStackRecordFor(ctx, ssmc, ClassPreview, "proj-1"); err != nil {
			t.Fatalf("DeleteStackRecordFor(preview): %v", err)
		}
		stillProd, err := ReadStackRecordFor(ctx, ssmc, ClassProduction, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackRecordFor(production) after preview delete: %v", err)
		}
		if stillProd.Edge.Secret != "prod-secret" {
			t.Errorf("production state was disturbed by a preview delete: %v", stillProd)
		}
	})

	t.Run("overwrites on rewrite", func(t *testing.T) {
		ssmc := newFakeSSM()
		ctx := context.Background()
		if err := WriteStackRecordFor(ctx, ssmc, ClassProduction, "proj-1", StackRecord{Edge: edge.StackState{Endpoint: "https://old"}}); err != nil {
			t.Fatalf("first WriteStackRecordFor: %v", err)
		}
		if err := WriteStackRecordFor(ctx, ssmc, ClassProduction, "proj-1", StackRecord{Edge: edge.StackState{Endpoint: "https://new"}}); err != nil {
			t.Fatalf("second WriteStackRecordFor: %v", err)
		}

		got, err := ReadStackRecord(ctx, ssmc, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackRecord: %v", err)
		}
		if got.Edge.Endpoint != "https://new" {
			t.Errorf("endpoint = %q, want the overwritten value %q", got.Edge.Endpoint, "https://new")
		}
	})
}

func TestDeleteStackRecord(t *testing.T) {
	t.Run("removes then reads absent", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := WriteStackRecordFor(context.Background(), ssmc, ClassProduction, "proj-1", StackRecord{Edge: edge.StackState{Endpoint: "https://store"}}); err != nil {
			t.Fatalf("WriteStackRecordFor: %v", err)
		}

		if err := DeleteStackRecord(context.Background(), ssmc, "proj-1"); err != nil {
			t.Fatalf("DeleteStackState: %v", err)
		}

		got, err := ReadStackRecord(context.Background(), ssmc, "proj-1")
		if err != nil {
			t.Fatalf("ReadStackRecord after delete: %v", err)
		}
		if !got.Empty() {
			t.Errorf("state after delete = %+v, want empty", got)
		}
	})

	t.Run("absent is idempotent success", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := DeleteStackRecord(context.Background(), ssmc, "proj-never-deployed"); err != nil {
			t.Fatalf("DeleteStackState on an absent parameter: %v, want nil (idempotent)", err)
		}
	})
}

func TestStackRecordFor(t *testing.T) {
	t.Run("unknown class errors", func(t *testing.T) {
		if _, err := ReadStackRecordFor(context.Background(), newFakeSSM(), "nonsense", "proj-1"); err == nil {
			t.Error("ReadStackRecordFor(unknown class) = nil error, want an error")
		}
	})
}
