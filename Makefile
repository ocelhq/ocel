.DEFAULT_GOAL := all

LAYER_DIR := dist/layer
LAYER_ZIP := dist/ocel-membrane-layer.zip

.PHONY: all generate cli provider proto layer publish-layer clean

all: cli provider layer lib

generate: proto

lib: 
	pnpm -F=ocel -F=@framework/next-adapter -F=@ocel/worker-nextjs -F=@ocel/worker-deployments-store build

cli:
	node scripts/build-native.mjs --host --target cli

provider:
	node scripts/build-native.mjs --host --target provider

proto:
	pnpm gen

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

publish-layer: layer
	aws lambda publish-layer-version \
	  --region us-east-1 \
	  --layer-name ocel-membrane \
	  --zip-file fileb://$(LAYER_ZIP) \
	  --compatible-runtimes nodejs24.x \
	  --compatible-architectures x86_64 \
	  --query LayerVersionArn --output text

clean:
	rm -rf dist
