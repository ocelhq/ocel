package payloads

import (
	"embed"
	"fmt"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/gcp-payloads

//go:embed dist
var embedded embed.FS

const ContainerArch = "amd64"

var (
	nodeRuntime      = load("dist/serve.mjs")
	containerRuntime = load("dist/container-runtime-" + ContainerArch)
)

func NodeRuntime() []byte { return nodeRuntime }

func ContainerRuntime(arch string) ([]byte, error) {
	if arch != ContainerArch {
		return nil, fmt.Errorf("this provider ships no container runtime built for %q: Cloud Run runs %s alone", arch, ContainerArch)
	}
	return containerRuntime, nil
}

func load(name string) []byte {
	body, err := embedded.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return body
}
