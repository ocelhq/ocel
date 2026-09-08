package gcp

import (
	"net"
	"os"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
	host := hostPort(endpoint)
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

func EmulatorREST(endpoint string) []option.ClientOption {
	if endpoint == "" {
		return nil
	}
	return []option.ClientOption{option.WithEndpoint(endpoint), option.WithoutAuthentication()}
}

func EmulatorGRPC(endpoint string) []option.ClientOption {
	if endpoint == "" {
		return nil
	}
	return []option.ClientOption{
		option.WithEndpoint(hostPort(endpoint)),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
}

func hostPort(endpoint string) string {
	authority := strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
	authority, _, _ = strings.Cut(authority, "/")
	return authority
}

func EmulatorStorage(endpoint string) []option.ClientOption {
	if endpoint == "" {
		return nil
	}
	return []option.ClientOption{
		option.WithEndpoint(endpoint + "/storage/v1/"),
		option.WithoutAuthentication(),
	}
}
