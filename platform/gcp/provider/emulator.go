package gcp

import (
	"os"
	"strings"

	"google.golang.org/api/option"
)

const emulatorEndpointVariable = "OCEL_FLOCI_GCP_ENDPOINT"

func emulatorEndpoint() string { return strings.TrimSpace(os.Getenv(emulatorEndpointVariable)) }

func restOptions(endpoint string) []option.ClientOption {
	if endpoint == "" {
		return nil
	}
	return []option.ClientOption{option.WithEndpoint(endpoint), option.WithoutAuthentication()}
}
