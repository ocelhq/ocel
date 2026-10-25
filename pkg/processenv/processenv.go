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

const NextPublicEnvVar = "NEXT_PUBLIC_OCEL_ENV"

const SvelteKitPublicURLEnvVar = "PUBLIC_OCEL_URL"

// TODO: this binds to Vercel's internals: Next's metadataBase fallback for social images
// reads only Vercel's variables (next/dist/lib/metadata/resolvers/resolve-url.js). Drop it
// for whatever Next reads once it offers a platform-neutral fallback, and recheck that
// fallback whenever the pinned Next version moves.
const NextMetadataBaseHostEnvVar = "VERCEL_PROJECT_PRODUCTION_URL"

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
	case NextPublicURLEnvVar, NextMetadataBaseHostEnvVar:
		return framework == buildoutput.FrameworkNext
	case SvelteKitPublicURLEnvVar:
		return framework == buildoutput.FrameworkSvelteKit
	}
	return false
}
