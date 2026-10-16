package realtime_test

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"ocel.dev/internal/sdktest"
	"ocel.dev/realtime"
)

type caller struct {
	ID string `json:"id"`
}

type orderParams struct {
	OrderID string
}

type orderEvent struct {
	Status string `json:"status"`
}

type deployParams struct {
	Project string `realtime:"projectId"`
	Deploy  string `realtime:"deployId"`
}

type roomParams struct {
	RoomID string
}

type chatMessage struct {
	Text string `json:"text"`
}

func authorizeByHeader(r *http.Request) (*caller, error) {
	if user := r.Header.Get("X-User"); user != "" {
		return &caller{ID: user}, nil
	}
	return nil, nil
}

func TestRealtimeDeclaresEachChannelsPatternWildcardSchemaAndAccessWhereItIsWritten(t *testing.T) {
	seen := sdktest.Declarations(t)

	_, file, line, _ := runtime.Caller(0)
	resource := realtime.New("app", realtime.Authorize(authorizeByHeader), realtime.TokenTTL(30*time.Second))
	realtime.NewChannel[orderEvent, orderParams](resource, "orders/:orderId",
		realtime.ChannelSubscribe(func(c *realtime.SubscribeContext[caller, orderParams]) (bool, error) { return true, nil }))
	realtime.NewChannel[orderEvent, deployParams](resource, "projects/:projectId/deploys/:deployId", realtime.ChannelWildcard(),
		realtime.ChannelSubscribe(func(c *realtime.SubscribeContext[caller, deployParams]) (bool, error) { return true, nil }))
	realtime.NewChannel[chatMessage, roomParams](resource, "rooms/:roomId", realtime.Schema(`{"type":"object"}`),
		realtime.ChannelSubscribe(func(c *realtime.SubscribeContext[caller, roomParams]) (bool, error) { return true, nil }),
		realtime.ChannelPublish(func(c *realtime.PublishContext[caller, roomParams, chatMessage]) (bool, error) { return true, nil }))
	realtime.NewChannel[orderEvent, struct{}](resource, "status", realtime.ChannelSubscribePublic())

	if resource.Name() != "app" {
		t.Errorf("Name() = %q, want app", resource.Name())
	}
	if len(*seen) != 5 {
		t.Fatalf("declares = %d, want the resource declared once and again with each channel", len(*seen))
	}
	last := sdktest.Declared(t, (*seen)[4:], "RESOURCE_TYPE_REALTIME", "app")
	if last["source"] != fmt.Sprintf("%s:%d", file, line+1) {
		t.Errorf("source = %v, want the line realtime.New was called on", last["source"])
	}
	config, _ := last["realtime"].(map[string]any)
	if config["tokenTtl"] != "30s" {
		t.Errorf("tokenTtl = %v, want 30s", config["tokenTtl"])
	}
	channels, _ := config["channels"].([]any)
	want := []struct {
		pattern, subscribe, publish, schema string
		wildcard                            bool
		line                                int
	}{
		{"orders/:orderId", "REALTIME_SUBSCRIBE_RULE", "REALTIME_PUBLISH_SERVER", "", false, line + 2},
		{"projects/:projectId/deploys/:deployId", "REALTIME_SUBSCRIBE_RULE", "REALTIME_PUBLISH_SERVER", "", true, line + 4},
		{"rooms/:roomId", "REALTIME_SUBSCRIBE_RULE", "REALTIME_PUBLISH_RULE", `{"type":"object"}`, false, line + 6},
		{"status", "REALTIME_SUBSCRIBE_PUBLIC", "REALTIME_PUBLISH_SERVER", "", false, line + 9},
	}
	if len(channels) != len(want) {
		t.Fatalf("channels = %v, want %d", channels, len(want))
	}
	for i, expected := range want {
		channel, _ := channels[i].(map[string]any)
		wildcard, _ := channel["wildcard"].(bool)
		schema, _ := channel["schema"].(string)
		if channel["pattern"] != expected.pattern || channel["subscribe"] != expected.subscribe || channel["publish"] != expected.publish || wildcard != expected.wildcard || schema != expected.schema {
			t.Errorf("channels[%d] = %v, want %+v", i, channel, expected)
		}
		if channel["source"] != fmt.Sprintf("%s:%d", file, expected.line) {
			t.Errorf("channels[%d].source = %v, want line %d", i, channel["source"], expected.line)
		}
	}
}

func TestARealtimeDeclarationOutsideTheContractIsRefusedSayingWhy(t *testing.T) {
	sdktest.Declarations(t)

	resource := realtime.New("refusals", realtime.Authorize(authorizeByHeader))
	for reason, declare := range map[string]func(){
		"outside 10s to 5m0s": func() { realtime.New("short", realtime.TokenTTL(5*time.Second)) },
		"channel namespace":   func() { realtime.New("my_app") },
		"twice": func() {
			realtime.NewChannel[orderEvent, struct{}](resource, "a/:x/:x", realtime.ChannelSubscribePublic())
		},
		"no subscribe": func() { realtime.NewChannel[orderEvent, orderParams](resource, "orders/:orderId") },
		"no field for parameter": func() {
			realtime.NewChannel[orderEvent, struct{}](resource, "orders/:orderId", realtime.ChannelSubscribePublic())
		},
		"rule whose params are": func() {
			realtime.NewChannel[orderEvent, orderParams](resource, "orders/:orderId",
				realtime.ChannelSubscribe(func(c *realtime.SubscribeContext[caller, roomParams]) (bool, error) { return true, nil }))
		},
		"authorize answers": func() {
			realtime.NewChannel[orderEvent, orderParams](resource, "orders/:orderId",
				realtime.ChannelSubscribe(func(c *realtime.SubscribeContext[string, orderParams]) (bool, error) { return true, nil }))
		},
		"schema that is no JSON": func() {
			realtime.NewChannel[orderEvent, struct{}](resource, "bad-json", realtime.ChannelSubscribePublic(), realtime.Schema(`{`))
		},
		"schema that does not compile": func() {
			realtime.NewChannel[orderEvent, struct{}](resource, "bad-schema", realtime.ChannelSubscribePublic(), realtime.Schema(`{"type":7}`))
		},
		"publish rule whose body is": func() {
			realtime.NewChannel[orderEvent, roomParams](resource, "rooms/:roomId", realtime.ChannelSubscribePublic(),
				realtime.ChannelPublish(func(c *realtime.PublishContext[caller, roomParams, chatMessage]) (bool, error) { return true, nil }))
		},
	} {
		if message := sdktest.PanicMessage(declare); !strings.Contains(message, reason) {
			t.Errorf("panic = %q, want it to say %q", message, reason)
		}
	}
}
