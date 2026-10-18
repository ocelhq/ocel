package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"google.golang.org/api/option"

	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime/pubsubbus"
	"github.com/ocelhq/ocel/platform/realtime/gatewayserver"
)

const subscribeTimeout = 2 * time.Minute

func main() {
	os.Exit(gatewayserver.Run(os.Args[1:], openBus(os.Getenv)))
}

func openBus(getenv func(string) string) func(context.Context) (gatewayserver.Bus, error) {
	return func(ctx context.Context) (gatewayserver.Bus, error) {
		topic := getenv(pubsubbus.TopicVar)
		if topic == "" {
			return nil, fmt.Errorf("%s names no Pub/Sub topic, and a gateway instance hears the publishes its siblings take through one alone", pubsubbus.TopicVar)
		}
		var options []option.ClientOption
		if endpoint := getenv(pubsubbus.EndpointVar); endpoint != "" {
			options = append(options, option.WithEndpoint(endpoint), option.WithoutAuthentication())
		}
		subscribing, cancel := context.WithTimeout(ctx, subscribeTimeout)
		defer cancel()
		return pubsubbus.Open(subscribing, topic, options...)
	}
}
