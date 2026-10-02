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
	emulatorEndpointVariable = "OCEL_FLOCI_GCP_ENDPOINT"
	hostFromContainers       = "host.docker.internal"
)

func emulatorEndpoint() (string, error) {
	endpoint := strings.TrimSpace(os.Getenv(emulatorEndpointVariable))
	if endpoint == "" || loopback(endpoint) {
		return endpoint, nil
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"%s names %s, and an emulator is addressed with no credentials at all: only a loopback address may be named there",
		emulatorEndpointVariable, endpoint)
}

func loopback(endpoint string) bool {
	host := ports.HostPort(endpoint)
	if named, _, err := net.SplitHostPort(host); err == nil {
		host = named
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (p *Provider) containerEndpoint() string {
	at, err := url.Parse(p.endpoint)
	if err != nil || at.Host == "" {
		return p.endpoint
	}
	at.Host = net.JoinHostPort(hostFromContainers, at.Port())
	return at.String()
}
