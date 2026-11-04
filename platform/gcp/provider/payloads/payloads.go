package payloads

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"sync"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/gcp-payloads

const ContainerArch = "amd64"

var (
	//go:embed dist/serve.mjs
	nodeRuntime []byte
	//go:embed dist/container-runtime-amd64
	containerRuntime []byte
	//go:embed dist/envsourcesync-amd64
	envSourceSync []byte
	//go:embed dist/realtime-gateway-amd64
	realtimeGateway []byte
	//go:embed dist/bastion-amd64
	bastion []byte
	//go:embed dist/gate.wasm
	gatePlugin []byte
	//go:embed dist/next
	nextFiles embed.FS
)

func NodeRuntime() []byte { return nodeRuntime }

var nextRuntime struct {
	once  sync.Once
	files map[string][]byte
}

func NextRuntime() map[string][]byte {
	nextRuntime.once.Do(func() { nextRuntime.files = readNextRuntime() })
	return nextRuntime.files
}

func readNextRuntime() map[string][]byte {
	files := map[string][]byte{}
	err := fs.WalkDir(nextFiles, "dist/next", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := nextFiles.ReadFile(name)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(name, "dist/next/")] = body
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return files
}

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
