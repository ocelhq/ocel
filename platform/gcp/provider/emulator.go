package gcp

import (
	"net"
	"os"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const emulatorEndpointVariable = "OCEL_FLOCI_GCP_ENDPOINT"

func emulatorEndpoint() (string, error) {
	endpoint := strings.TrimSpace(os.Getenv(emulatorEndpointVariable))
	if endpoint == "" || loopback(endpoint) {
		return endpoint, nil
	}
	return "", providerkit.Refuse(providerkit.CodeInvalid,
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
