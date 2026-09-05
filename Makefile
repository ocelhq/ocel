.DEFAULT_GOAL := all

.PHONY: all generate cli provider proto clean lint

all: cli provider lib

generate: proto

lib:
	pnpm turbo run build --filter=ocel --filter=@cli/node

cli:
	node scripts/build-native.mjs --host --target cli

provider:
	node scripts/build-native.mjs --host --target provider-aws
	node scripts/build-native.mjs --host --target provider-vps

proto:
	pnpm gen

clean:
	rm -rf platform/aws/provider/payloads/dist cli/node/dist

lint:
	pnpm exec biome check
	for dir in $$(go work edit -json | node -p 'JSON.parse(require("fs").readFileSync(0)).Use.map(u => u.DiskPath).join(" ")'); do (cd "$$dir" && golangci-lint run ./...) || exit 1; done
