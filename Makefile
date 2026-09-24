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
	vettool="$$(mktemp -d)"; trap 'rm -rf "$$vettool"' EXIT; \
	go build -o "$$vettool/redactvet" ./scripts/redactvet || exit 1; \
	for dir in $$(go list -m -f '{{.Dir}}'); do \
		(cd "$$dir" && golangci-lint run ./... && go vet -vettool="$$vettool/redactvet" ./...) || exit 1; \
	done
