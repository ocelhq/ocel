package gcp

import (
	"os"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const emulatorEndpointVariable = "OCEL_FLOCI_GCP_ENDPOINT"

func emulatorEndpoint() string { return strings.TrimSpace(os.Getenv(emulatorEndpointVariable)) }

func restOptions(endpoint string) []option.ClientOption {
	if endpoint == "" {
		return nil
	}
	return []option.ClientOption{option.WithEndpoint(endpoint), option.WithoutAuthentication()}
}

func grpcOptions(endpoint string) []option.ClientOption {
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
	return strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
}
