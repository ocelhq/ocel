# Ocel build orchestration.
#
# Wraps the generation and build steps scattered across the repo behind named
# targets. Binary builds delegate to scripts/build-native.mjs (the single
# source of truth for the Go platform matrix); codegen delegates to pnpm/buf
# and go generate. The membrane layer is built and published here.

.DEFAULT_GOAL := all

LAYER_DIR := dist/layer
LAYER_ZIP := dist/ocel-membrane-layer.zip

.PHONY: all generate cli provider proto layer publish-layer clean

# ---- Aggregates ----------------------------------------------------------

# Local build of every artifact (no AWS side effects; publish-layer is opt-in).
all: cli provider layer lib

# All codegen: proto bindings. (The node builder ships in the ocel npm package,
# built via `pnpm --filter ocel build`, not go generate.)
generate: proto

lib: 
	pnpm -F=@ocel/sdk -F=ocel -F=@ocel/next-runtime build

# ---- Binaries ------------------------------------------------------------

# The `ocel` CLI for the host platform.
cli:
	node scripts/build-native.mjs --host --target cli

# The AWS provider distribution (deploy + runtime) for the host platform.
provider:
	node scripts/build-native.mjs --host --target provider

# ---- Codegen -------------------------------------------------------------

# Regenerate proto bindings (Go + TS) from proto/.
proto:
	pnpm gen

# ---- Membrane layer ------------------------------------------------------

# Build the lambdanode bootstrap (linux/amd64) and bundle it with the compiled
# node entrypoint into the layer zip. No AWS calls — publishing is separate.
layer:
	pnpm --filter @ocel/lambda-entrypoints build
	rm -rf $(LAYER_DIR)/ocel
	mkdir -p $(LAYER_DIR)/ocel
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -tags lambda.norpc -ldflags="-s -w" \
	  -o $(CURDIR)/$(LAYER_DIR)/ocel/bootstrap ./cloud/aws/cmd/lambdanode/bootstrap
	chmod +x $(LAYER_DIR)/ocel/bootstrap
	cp -R packages/lambda-entrypoints/dist/. $(LAYER_DIR)/ocel/
	rm -f $(LAYER_ZIP)
	cd $(LAYER_DIR) && zip -r $(CURDIR)/$(LAYER_ZIP) ocel

# Publish the built layer to AWS and print the new version ARN. Paste it into
# defaultMembraneLayerARN in cloud/aws/deploy/function.go. Needs AWS creds for
# the Ocel account; region/name are pinned to the shared membrane layer.
publish-layer: layer
	aws lambda publish-layer-version \
	  --region us-east-1 \
	  --layer-name ocel-membrane \
	  --zip-file fileb://$(LAYER_ZIP) \
	  --compatible-runtimes nodejs24.x \
	  --compatible-architectures x86_64 \
	  --query LayerVersionArn --output text

# ---- Housekeeping --------------------------------------------------------

clean:
	rm -rf dist
