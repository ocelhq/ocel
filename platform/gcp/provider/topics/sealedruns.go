package topics

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	payloadField = "payload"
	outputField  = "output"
)

type storedRun struct {
	runs.Run
	payload []byte
	output  []byte
}

func (s Store) bound(field string, run runs.Run) seal.AssociatedData {
	return seal.BindToProject(s.Scope.Slug, "run "+field, string(s.Scope.Tier), s.Scope.Environment, run.Topic, run.Execution)
}

func (s Store) sealField(ctx context.Context, field string, run runs.Run, value json.RawMessage) ([]byte, error) {
	if len(value) == 0 {
		return nil, nil
	}
	sealed, err := s.Cipher.Seal(ctx, s.Scope.Tier, s.bound(field, run), value)
	if err != nil {
		return nil, fmt.Errorf("seal the %s of run %s: %w", field, run.Execution, err)
	}
	return sealed, nil
}

func (s Store) openField(ctx context.Context, field string, run runs.Run, sealed []byte) (json.RawMessage, error) {
	if len(sealed) == 0 {
		return nil, nil
	}
	opened, err := s.Cipher.Open(ctx, s.Scope.Tier, s.bound(field, run), sealed)
	if err != nil {
		return nil, fmt.Errorf("open the %s of run %s, which was not sealed for it: %w", field, run.Execution, err)
	}
	return opened, nil
}

func (s Store) sealedDocumentOf(ctx context.Context, run runs.Run, revision records.Revision) (runDocument, error) {
	doc := documentOf(run, revision)
	var err error
	if doc.Payload, err = s.sealField(ctx, payloadField, run, run.Payload); err != nil {
		return runDocument{}, err
	}
	if doc.Output, err = s.sealField(ctx, outputField, run, run.Output); err != nil {
		return runDocument{}, err
	}
	return doc, nil
}

func (s Store) opened(ctx context.Context, stored storedRun) (runs.Run, error) {
	run := stored.Run
	var err error
	if run.Payload, err = s.openField(ctx, payloadField, run, stored.payload); err != nil {
		return runs.Run{}, err
	}
	if run.Output, err = s.openField(ctx, outputField, run, stored.output); err != nil {
		return runs.Run{}, err
	}
	return run, nil
}
