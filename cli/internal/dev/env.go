package dev

import (
	"cmp"
	"os"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
)

const (
	hostEnv     = "HOST"
	portEnv     = "PORT"
	defaultPort = "3000"
)

type runtimeAccess struct {
	url   string
	token string
}

func resolvedEnv(secretValues, values map[string]string, resources []binding.Resolved, runtime runtimeAccess, appFolder string, scope variables.Scope) map[string]string {
	merged := make(map[string]string, len(secretValues)+len(values)+1)
	for k, v := range secretValues {
		merged[k] = v
	}
	for k, v := range values {
		merged[k] = v
	}
	for _, r := range resources {
		for k, v := range r.Env {
			merged[k] = v
		}
	}
	if runtime.url != "" {
		merged[processenv.RuntimeAddressEnvVar] = runtime.url
		merged[localrpc.SessionTokenEnvVar] = runtime.token
	}
	merged[processenv.AppFolderEnvVar] = appFolder
	merged[processenv.AppURLEnvVar] = localURL(merged[portEnv])
	if scope.IsWrittenByOcel(processenv.ClientURLEnvVar, nil) {
		merged[processenv.ClientURLEnvVar] = merged[processenv.AppURLEnvVar]
	}
	return merged
}

func localURL(port string) string {
	return "http://localhost:" + cmp.Or(port, os.Getenv(portEnv), defaultPort)
}

func applyEnv(base []string, overrides map[string]string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i >= 0 && !strings.HasPrefix(kv, processenv.ResourceEnvVarPrefix) {
			merged[kv[:i]] = kv[i+1:]
		}
	}
	for k, v := range overrides {
		merged[k] = v
	}

	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}
