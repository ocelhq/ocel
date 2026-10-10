package processenv

import (
	"os"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const PhaseEnvVar = "OCEL_PHASE"

const DevServerEnvVar = "OCEL_DEV_SERVER"

const DevServerTokenEnvVar = "OCEL_DEV_SERVER_TOKEN"

const AppFolderEnvVar = "OCEL_APP_FOLDER"

const AppURLEnvVar = "OCEL_URL"

const RuntimeAddressEnvVar = "OCEL_RUNTIME_ADDRESS"

const WorkerEnvVar = "OCEL_WORKER"

const LiveKeysEnvVar = "OCEL_LIVE_KEYS"

const ResourceEnvVarPrefix = "OCEL_RESOURCE_"

const LiveDirEnvVar = "OCEL_LIVE_DIR"

const NextPublicURLEnvVar = "NEXT_PUBLIC_OCEL_URL"

const SvelteKitPublicURLEnvVar = "PUBLIC_OCEL_URL"

const PublicKeysEnvVar = "OCEL_PUBLIC_KEYS"

const NextAdapterPathEnvVar = "NEXT_ADAPTER_PATH"

const NextPublicPrefix = "NEXT_PUBLIC_"

const SkipChecksEnvVar = "OCEL_SKIP_CHECKS"

func SkipChecks() bool {
	switch strings.ToLower(os.Getenv(SkipChecksEnvVar)) {
	case "1", "true":
		return true
	}
	return false
}

func IsNextPublic(key string) bool {
	return strings.HasPrefix(key, NextPublicPrefix)
}

func IsInjected(framework, key string) bool {
	switch key {
	case AppURLEnvVar:
		return true
	case NextPublicURLEnvVar:
		return framework == buildoutput.FrameworkNext
	case SvelteKitPublicURLEnvVar:
		return framework == buildoutput.FrameworkSvelteKit
	}
	return false
}
