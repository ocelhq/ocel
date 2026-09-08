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
	return strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
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
