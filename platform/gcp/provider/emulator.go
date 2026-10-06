package gcp

import (
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	emulatorEndpointVariable    = "OCEL_FLOCI_GCP_ENDPOINT"
	tagEmulatorEndpointVariable = "OCEL_FLOCI_FIRESTORE_ENDPOINT"
	hostFromContainers          = "host.docker.internal"
)

func emulatorEndpoint() (string, error) {
	endpoint := strings.TrimSpace(os.Getenv(emulatorEndpointVariable))
	if endpoint == "" || ports.IsLoopback(endpoint) {
		return endpoint, nil
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"%s names %s, and an emulator is addressed with no credentials at all: only a loopback address may be named there",
		emulatorEndpointVariable, endpoint)
}

func tagEmulatorEndpoint(flociEndpoint string) (string, error) {
	endpoint := strings.TrimSpace(os.Getenv(tagEmulatorEndpointVariable))
	if endpoint == "" {
		return "", nil
	}
	if flociEndpoint == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s names %s, and the Firestore emulator for tag records runs only beside the floci-gcp emulator %s names: set both or neither",
			tagEmulatorEndpointVariable, endpoint, emulatorEndpointVariable)
	}
	if !ports.IsLoopback(endpoint) {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s names %s, and an emulator is addressed with no credentials at all: only a loopback address may be named there",
			tagEmulatorEndpointVariable, endpoint)
	}
	return endpoint, nil
}

func containerAddress(endpoint string) string {
	at, err := url.Parse(endpoint)
	if err != nil || at.Host == "" {
		return endpoint
	}
	at.Host = net.JoinHostPort(hostFromContainers, at.Port())
	return at.String()
}

func (p *Provider) containerEndpoint() string { return containerAddress(p.endpoint) }

func (p *Provider) containerTagEndpoint() string {
	if p.tagEndpoint == "" {
		return p.containerEndpoint()
	}
	return containerAddress(p.tagEndpoint)
}
