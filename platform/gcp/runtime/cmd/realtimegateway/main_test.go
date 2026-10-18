package main

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime/pubsubbus"
)

func TestAGatewayNamedNoTopicIsRefusedRatherThanRunAlone(t *testing.T) {
	t.Parallel()

	if bus, err := openBus(func(string) string { return "" })(context.Background()); err == nil {
		_ = bus.Close(context.Background())
		t.Errorf("openBus() with no %s = nil, want an error: an instance on no topic never hears a publish another instance takes", pubsubbus.TopicVar)
	}
}

func TestAGatewayNamedSomethingOtherThanATopicIsRefused(t *testing.T) {
	t.Parallel()

	env := map[string]string{pubsubbus.TopicVar: "ocel-shop-prod-realtime", pubsubbus.EndpointVar: "http://127.0.0.1:1"}
	if bus, err := openBus(func(name string) string { return env[name] })(context.Background()); err == nil {
		_ = bus.Close(context.Background())
		t.Error("openBus() with a bare topic id = nil, want an error naming the projects/<project>/topics/<topic> it wants")
	}
}
