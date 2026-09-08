package payloads

import (
	"embed"
	"fmt"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/gcp-payloads

//go:embed dist
var embedded embed.FS

var nodeMembrane = load("dist/serve.mjs")

func NodeMembrane() []byte { return nodeMembrane }

func load(name string) []byte {
	body, err := embedded.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return body
}
