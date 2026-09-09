package payloads

import (
	"embed"
	"fmt"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/gcp-payloads

//go:embed dist
var embedded embed.FS

var nodeRuntime = load("dist/serve.mjs")

func NodeRuntime() []byte { return nodeRuntime }

func load(name string) []byte {
	body, err := embedded.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return body
}
