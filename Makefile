.DEFAULT_GOAL := all

.PHONY: all generate cli provider proto services clean

all: cli provider lib

generate: proto

lib:
	pnpm -F=ocel -F=@framework/next-adapter -F=@platform/cf-entry -F=@platform/cf-deployments-store build

cli:
	node scripts/build-native.mjs --host --target cli

provider:
	node scripts/build-native.mjs --host --target provider

proto:
	pnpm gen

services:
	node scripts/dev-services.mjs

clean:
	rm -rf platform/aws/provider/payloads/dist cli/node/dist
