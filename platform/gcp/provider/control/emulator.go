package control

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
)

func emulatorEndpoint() (string, error) {
	return loopbackEmulatorEndpoint(emulatorEndpointVariable)
}

func loopbackEmulatorEndpoint(variable string) (string, error) {
	endpoint := strings.TrimSpace(os.Getenv(variable))
	if endpoint == "" || ports.IsLoopback(endpoint) {
		return endpoint, nil
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"%s names %s, and an emulator is addressed with no credentials at all: only a loopback address may be named there",
		variable, endpoint)
}

func tagEmulatorEndpoint(flociEndpoint string) (string, error) {
	endpoint, err := loopbackEmulatorEndpoint(tagEmulatorEndpointVariable)
	if err != nil || endpoint == "" {
		return "", err
	}
	if flociEndpoint == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s names %s, and the Firestore emulator for tag records runs only beside the floci-gcp emulator %s names: set both or neither",
			tagEmulatorEndpointVariable, endpoint, emulatorEndpointVariable)
	}
	return endpoint, nil
}

const hostFromContainers = "host.docker.internal"

func containerAddress(endpoint string) string {
	at, err := url.Parse(endpoint)
	if err != nil || at.Host == "" {
		return endpoint
	}
	at.Host = net.JoinHostPort(hostFromContainers, at.Port())
	return at.String()
}

func (p *Provider) ContainerEndpoint() string { return containerAddress(p.Emulator) }

func (p *Provider) ContainerTagEndpoint() string {
	if p.TagEmulator == "" {
		return p.ContainerEndpoint()
	}
	return containerAddress(p.TagEmulator)
}
