package infra

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	"ocel.dev"
)

type Sighting struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
}

var errNoRun = errors.New("the handler was handed a context without a run")

func init() {
	renameBindings(taskBindingKey(ExactEcho.Name()), topicBindingKey(ExactOrders.Name()))
	if err := recordRequests(); err != nil {
		log.Fatalf("record the requests this process sends the ocel runtime: %v", err)
	}
	if err := recordEnvelopes(); err != nil {
		log.Fatalf("record the envelopes this worker is delivered: %v", err)
	}
}

var SightingTask = ocel.Task("sighting", func(_ context.Context, sighting Sighting) (Sighting, error) {
	return sighting, nil
})

func recordSighting(ctx context.Context, tag string, payload json.RawMessage) error {
	run, ok := ocel.RunFrom(ctx)
	if !ok {
		return errNoRun
	}
	if tag == "" {
		tag = run.ID
	}
	sighting := Sighting{
		Kind: string(run.Kind), Name: run.Name, Topic: run.Topic,
		Payload: string(payload),
	}
	_, err := SightingTask.Trigger(ctx, sighting, ocel.Tags(tag))
	return err
}

var ExactEcho = ocel.Task("exact-echo", func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	return payload, recordSighting(ctx, "", payload)
})

var ExactOrders = ocel.Topic[json.RawMessage]("exact-orders")

var ExactAudit = ExactOrders.Consumer("exact-audit", func(ctx context.Context, payload json.RawMessage) error {
	run, ok := ocel.RunFrom(ctx)
	if !ok {
		return errNoRun
	}
	return recordSighting(ctx, run.Message.ID, payload)
})
