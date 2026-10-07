package payloads

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/gcp-payloads

//go:embed dist
var embedded embed.FS

const ContainerArch = "amd64"

var (
	nodeRuntime       = load("dist/serve.mjs")
	containerRuntime  = load("dist/container-runtime-" + ContainerArch)
	envSourceSync     = load("dist/envsourcesync-" + ContainerArch)
	realtimeGateway   = load("dist/realtime-gateway-" + ContainerArch)
	bastion           = load("dist/bastion-" + ContainerArch)
	nextServerRuntime = loadNextServerRuntime()
)

var nextServerRuntimeFiles = []string{containerimage.NextServerAdapterFile, "cache-handler.cjs", "use-cache-default.cjs", "use-cache-remote.cjs"}

func NodeRuntime() []byte { return nodeRuntime }

func NextRuntime() fs.FS {
	directory, err := fs.Sub(embedded, "dist/next")
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return directory
}

func NextServerRuntime() map[string][]byte { return nextServerRuntime }

func ContainerRuntime(arch string) ([]byte, error) {
	if arch != ContainerArch {
		return nil, fmt.Errorf("this provider ships no container runtime built for %q: Cloud Run runs %s alone", arch, ContainerArch)
	}
	return containerRuntime, nil
}

func EnvSourceSync() []byte { return envSourceSync }

func RealtimeGateway() []byte { return realtimeGateway }

func Bastion() []byte { return bastion }

func load(name string) []byte {
	body, err := embedded.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return body
}

func loadNextServerRuntime() map[string][]byte {
	files := make(map[string][]byte, len(nextServerRuntimeFiles))
	for _, name := range nextServerRuntimeFiles {
		files[name] = load("dist/next/" + name)
	}
	return files
}
