package infra

import (
	"context"
	"encoding/json"
	"errors"

	"ocel.dev"
)

type Seen struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
}

var errNoRun = errors.New("the handler was handed a context without a run")

var SeenTask = ocel.Task("seen", func(_ context.Context, seen Seen) (Seen, error) {
	return seen, nil
})

func recordSeen(ctx context.Context, tag string, payload json.RawMessage) error {
	run, ok := ocel.RunFrom(ctx)
	if !ok {
		return errNoRun
	}
	if tag == "" {
		tag = run.ID
	}
	seen := Seen{Kind: string(run.Kind), Name: run.Name, Topic: run.Topic, Payload: string(payload)}
	_, err := SeenTask.Trigger(ctx, seen, ocel.Tags(tag))
	return err
}

var ExactEcho = ocel.Task("exact-echo", func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	return payload, recordSeen(ctx, "", payload)
})

var ExactOrders = ocel.Topic[json.RawMessage]("exact-orders")

var ExactAudit = ExactOrders.Consumer("exact-audit", func(ctx context.Context, payload json.RawMessage) error {
	run, ok := ocel.RunFrom(ctx)
	if !ok {
		return errNoRun
	}
	return recordSeen(ctx, run.Message.ID, payload)
})
