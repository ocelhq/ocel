package bundles

import (
	"embed"
	"fmt"
)

//go:generate pnpm --dir ../../../../.. exec turbo run generate --filter=@platform/cf-bundles

//go:embed dist
var embedded embed.FS

var (
	entry            = load("dist/entry.js")
	deploymentsStore = load("dist/deployments-store.js")
	isrWriter        = load("dist/isr-writer.js")
)

func Entry() []byte { return entry }

func DeploymentsStore() []byte { return deploymentsStore }

func ISRWriter() []byte { return isrWriter }

func load(name string) []byte {
	body, err := embedded.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("bundles: %v", err))
	}
	return body
}
