package provider_test

import (
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

const stagedPayloadBytes = 256 * 1024

func TestTheWireRefusesAPayloadAboveWhatTheTaskStoreStages(t *testing.T) {
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	for name, message := range map[string]func(payload []byte) proto.Message{
		"send":          func(payload []byte) proto.Message { return &topicv1.SendRequest{Topic: "orders", Payload: payload} },
		"trigger":       func(payload []byte) proto.Message { return &taskv1.TriggerRequest{Task: "resize", Payload: payload} },
		"batch trigger": func(payload []byte) proto.Message { return &taskv1.BatchTriggerItem{Payload: payload} },
	} {
		t.Run(name, func(t *testing.T) {
			if err := validator.Validate(message(make([]byte, stagedPayloadBytes))); err != nil {
				t.Errorf("a payload of exactly 256 KiB = %v, want it accepted", err)
			}
			if err := validator.Validate(message(make([]byte, stagedPayloadBytes+1))); err == nil {
				t.Error("a payload one byte above 256 KiB validated, want it refused on the wire before a store stages it")
			}
		})
	}
}
