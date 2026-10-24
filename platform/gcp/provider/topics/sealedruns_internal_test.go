package topics

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/runs"
)

func sealingStore() Store {
	return Store{Cipher: fake.NewCipher(), Scope: Scope{Slug: "shop", Tier: environment.TierProduction, Environment: "production"}}
}

func TestARunsPayloadAndOutputAreWrittenSealedAndOpenAsTheyWere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sealingStore()
	run := runs.Run{Execution: "e1", Topic: "resize", Status: provider.RunCompleted,
		Payload: json.RawMessage(`{"image":"cat.png"}`), Output: json.RawMessage(`{"width":640}`)}

	doc, err := s.sealedDocumentOf(ctx, run, "r1")
	if err != nil {
		t.Fatal(err)
	}
	for field, sealed := range map[string][]byte{"payload": doc.Payload, "output": doc.Output} {
		if len(sealed) == 0 || bytes.Contains(sealed, []byte("cat.png")) || bytes.Contains(sealed, []byte("640")) {
			t.Errorf("the run's %s is written as %q, want it sealed", field, sealed)
		}
	}
	opened, err := s.opened(ctx, storedRun{Run: runs.Run{Execution: "e1", Topic: "resize"}, payload: doc.Payload, output: doc.Output})
	if err != nil {
		t.Fatal(err)
	}
	if string(opened.Payload) != string(run.Payload) || string(opened.Output) != string(run.Output) {
		t.Errorf("the run opens with payload %s and output %s, want %s and %s", opened.Payload, opened.Output, run.Payload, run.Output)
	}
}

func TestAPayloadCopiedIntoAnotherRunOrProjectDoesNotOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := sealingStore()
	doc, err := s.sealedDocumentOf(ctx, runs.Run{Execution: "e1", Topic: "resize", Payload: json.RawMessage(`{"image":"cat.png"}`)}, "r1")
	if err != nil {
		t.Fatal(err)
	}

	other := s
	other.Scope.Slug = "blog"
	for name, attempt := range map[string]func() error{
		"another run of the topic": func() error {
			_, err := s.opened(ctx, storedRun{Run: runs.Run{Execution: "e2", Topic: "resize"}, payload: doc.Payload})
			return err
		},
		"another topic": func() error {
			_, err := s.opened(ctx, storedRun{Run: runs.Run{Execution: "e1", Topic: "thumbnail"}, payload: doc.Payload})
			return err
		},
		"another project": func() error {
			_, err := other.opened(ctx, storedRun{Run: runs.Run{Execution: "e1", Topic: "resize"}, payload: doc.Payload})
			return err
		},
		"the output of the same run": func() error {
			_, err := s.opened(ctx, storedRun{Run: runs.Run{Execution: "e1", Topic: "resize"}, output: doc.Payload})
			return err
		},
	} {
		if attempt() == nil {
			t.Errorf("a payload sealed for run e1 of shop's resize opened as %s, so a run may be handed a payload written for another", name)
		}
	}
}

func TestARunWithNoPayloadOrOutputSealsNothing(t *testing.T) {
	t.Parallel()
	doc, err := Store{Scope: Scope{Slug: "shop", Tier: environment.TierProduction}}.sealedDocumentOf(context.Background(), runs.Run{Execution: "e1", Topic: "resize"}, "r1")
	if err != nil {
		t.Fatalf("sealing a run with nothing to seal and no cipher = %v", err)
	}
	if doc.Payload != nil || doc.Output != nil {
		t.Errorf("a run with no payload or output wrote %q and %q, want neither", doc.Payload, doc.Output)
	}
}
