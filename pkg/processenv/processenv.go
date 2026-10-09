package processenv

import (
	"os"
	"strings"
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

const DeliveredVariablePrefix = "OCEL_VAR_"

const ClientURLEnvVar = "NEXT_PUBLIC_OCEL_URL"

const SkipChecksEnvVar = "OCEL_SKIP_CHECKS"

func SkipChecks() bool {
	switch strings.ToLower(os.Getenv(SkipChecksEnvVar)) {
	case "1", "true":
		return true
	}
	return false
}

func IsInjected(clientBundle bool, key string) bool {
	switch key {
	case AppURLEnvVar:
		return true
	case ClientURLEnvVar:
		return clientBundle
	}
	return false
}
