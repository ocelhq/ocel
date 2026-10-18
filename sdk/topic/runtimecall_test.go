package topic_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	"ocel.dev"
	"ocel.dev/internal/sdktest"
	"ocel.dev/topic"
)

func TestSendReturnsARuntimeCallErrorWhenTheRuntimeRefusesIt(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, sdktest.TopicBinding("orders"))
	runtime.Refusal = connect.NewError(connect.CodeNotFound, errors.New("the topic is not served here"))

	_, err := topic.New[order]("orders").Send(t.Context(), order{ID: "o-1"})

	var refused *ocel.RuntimeCallError
	if !errors.As(err, &refused) {
		t.Fatalf("Send() error = %v, want an *ocel.RuntimeCallError", err)
	}
	if refused.Code != "not_found" || refused.Procedure != "/app.topic.v1.TopicService/Send" {
		t.Errorf("RuntimeCallError = %+v, want not_found from /app.topic.v1.TopicService/Send", refused)
	}
}
