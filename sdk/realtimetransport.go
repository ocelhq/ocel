package ocel

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

type realtimeTransport struct {
	name           string
	answersHost    bool
	mintCredential func(d *RealtimeDefinition, binding realtimeBinding, channel string) (string, error)
	send           func(ctx context.Context, d *RealtimeDefinition, binding realtimeBinding, channel string, envelope []byte, credential string) error
}

var realtimePublishTimeout = 10 * time.Second

func findRealtimeTransport(transport bindingsv1.RealtimeTransport) (realtimeTransport, error) {
	switch transport {
	case bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_APPSYNC_EVENTS:
		return realtimeTransport{name: "appsync-events", answersHost: true, mintCredential: mintNoCredential, send: sendToAppSyncEvents}, nil
	case bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY:
		return realtimeTransport{name: "ocel-gateway", mintCredential: mintGatewayCredential, send: sendToGateway}, nil
	}
	return realtimeTransport{}, fmt.Errorf("the realtime binding names transport %s, which this SDK does not speak", transport)
}

func (d *RealtimeDefinition) mintPublishCredential(binding realtimeBinding, channel string) (string, error) {
	return binding.transport.mintCredential(d, binding, channel)
}

func (d *RealtimeDefinition) sendEvent(ctx context.Context, binding realtimeBinding, channel string, envelope []byte, credential string) error {
	ctx, cancel := context.WithTimeout(ctx, realtimePublishTimeout)
	defer cancel()
	return binding.transport.send(ctx, d, binding, channel, envelope, credential)
}

func buildGatewayPublishURL(socketURL string) (string, error) {
	address, err := url.Parse(socketURL)
	if err != nil {
		return "", fmt.Errorf("the realtime binding's url %q is no URL: %w", socketURL, err)
	}
	scheme := "http"
	if address.Scheme == "wss" {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: address.Host, Path: "/publish"}).String(), nil
}

func mintGatewayCredential(d *RealtimeDefinition, binding realtimeBinding, channel string) (string, error) {
	token, err := d.mintToken(binding, "server", realtimePublish, channel)
	return token.Token, err
}

func sendToGateway(ctx context.Context, d *RealtimeDefinition, binding realtimeBinding, channel string, envelope []byte, credential string) error {
	endpoint, err := buildGatewayPublishURL(binding.properties.GetUrl())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(envelope))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", bearer(credential))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("ocel: realtime %q: publish on %s: %w", d.name, channel, err)
	}
	_ = res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("ocel: realtime %q: the gateway refused a publish on %s with status %d", d.name, channel, res.StatusCode)
	}
	return nil
}

func mintNoCredential(*RealtimeDefinition, realtimeBinding, string) (string, error) { return "", nil }

func sendToAppSyncEvents(_ context.Context, d *RealtimeDefinition, _ realtimeBinding, _ string, _ []byte, _ string) error {
	// TODO(#1514): publish with a SigV4-signed POST /event under the app's role once the AWS target lands.
	return fmt.Errorf("ocel: realtime %q: publishing on AppSync Events is not supported yet", d.name)
}
