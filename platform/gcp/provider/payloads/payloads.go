package payloads

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/gcp-payloads

//go:embed dist
var embedded embed.FS

const ContainerArch = "amd64"

var (
	nodeRuntime      = load("dist/serve.mjs")
	containerRuntime = load("dist/container-runtime-" + ContainerArch)
	envSourceSync    = load("dist/envsourcesync-" + ContainerArch)
	realtimeGateway  = load("dist/realtime-gateway-" + ContainerArch)
	bastion          = load("dist/bastion-" + ContainerArch)
	gatePlugin       = load("dist/gate.wasm")
	nextRuntime      = loadNextRuntime()
)

func NodeRuntime() []byte { return nodeRuntime }

func NextRuntime() map[string][]byte { return nextRuntime }

func ContainerRuntime(arch string) ([]byte, error) {
	if arch != ContainerArch {
		return nil, fmt.Errorf("this provider ships no container runtime built for %q: Cloud Run runs %s alone", arch, ContainerArch)
	}
	return containerRuntime, nil
}

func EnvSourceSync() []byte { return envSourceSync }

func RealtimeGateway() []byte { return realtimeGateway }

func Bastion() []byte { return bastion }

func GatePlugin() []byte { return gatePlugin }

func load(name string) []byte {
	body, err := embedded.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return body
}

func loadNextRuntime() map[string][]byte {
	files := map[string][]byte{}
	err := fs.WalkDir(embedded, "dist/next", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		files[strings.TrimPrefix(name, "dist/next/")] = load(name)
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return files
}
