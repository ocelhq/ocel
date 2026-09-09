.DEFAULT_GOAL := all

.PHONY: all generate snapshot lib proto clean lint

all: snapshot

generate: proto

lib:
	pnpm turbo run build --filter=ocel --filter=@cli/node

snapshot:
	node scripts/snapshot.mjs

proto:
	pnpm gen

clean:
	rm -rf dist platform/aws/provider/payloads/dist cli/node/dist

lint:
	pnpm exec biome check
	for dir in $$(go work edit -json | node -p 'JSON.parse(require("fs").readFileSync(0)).Use.map(u => u.DiskPath).join(" ")'); do (cd "$$dir" && golangci-lint run ./...) || exit 1; done
