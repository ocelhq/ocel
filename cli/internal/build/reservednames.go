package build

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
)

var buildSetNames = []string{
	"PATH",
	"NODE_ENV",
	"NEXT_ADAPTER_PATH",
	"NEXT_DEPLOYMENT_ID",
	"CARGO_PROFILE_RELEASE_STRIP",
	processenv.AppFolderEnvVar,
	processenv.PhaseEnvVar,
	processenv.LiveDirEnvVar,
	processenv.RuntimeAddressEnvVar,
	localrpc.SessionTokenEnvVar,
	"OCEL_APP_NAME",
	"OCEL_OUTPUT_DIR",
	"OCEL_EDGE_KIND",
	"OCEL_ALLOW_DEGRADED",
	"OCEL_MAX_FUNCTION_BYTES",
}

var toolchainNames = []string{"HOME", "USERPROFILE", "TMPDIR", "TMP", "TEMP", "NODE_OPTIONS", "NODE_PATH"}

var toolchainPrefixes = []string{"CARGO_", "RUSTUP_", "npm_config_"}

func refuseReservedNames(app string, values AppVariables) error {
	for _, key := range slices.Sorted(maps.Keys(values.Env)) {
		if slices.Contains(buildSetNames, key) {
			return fmt.Errorf("app %q declares %s, which the build sets itself; rename it where it is declared", app, key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(values.Live)) {
		if slices.Contains(buildSetNames, key) {
			return fmt.Errorf("app %q declares %s, which the build sets itself; rename it where it is declared", app, key)
		}
		if isToolchainName(key) {
			return fmt.Errorf("app %q declares %s as sensitive or secret, which keeps it out of the build's environment, and node or cargo reads %s from there; rename it where it is declared", app, key, key)
		}
	}
	return nil
}

func isToolchainName(key string) bool {
	return slices.Contains(toolchainNames, key) || slices.ContainsFunc(toolchainPrefixes, func(prefix string) bool { return strings.HasPrefix(key, prefix) })
}
