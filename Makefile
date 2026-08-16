.DEFAULT_GOAL := all

LAYER_DIR := dist/layer
LAYER_ZIP := dist/ocel-membrane-layer.zip
LISTENER_DIR := dist/listener
LISTENER_ZIP := dist/ocel-listener.zip

.PHONY: all generate cli provider proto layer listener publish-layer clean

all: cli provider layer listener lib

generate: proto

lib: 
	pnpm -F=ocel -F=@framework/next-adapter -F=@platform/cf-entry -F=@platform/cf-deployments-store build

cli:
	node scripts/build-native.mjs --host --target cli

provider:
	node scripts/build-native.mjs --host --target provider

proto:
	pnpm gen

layer:
	pnpm --filter @platform/aws-entrypoints build
	rm -rf $(LAYER_DIR)/ocel
	mkdir -p $(LAYER_DIR)/ocel
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -tags lambda.norpc -ldflags="-s -w" \
	  -o $(CURDIR)/$(LAYER_DIR)/ocel/bootstrap ./platform/aws/provider/cmd/lambdanode/bootstrap
	chmod +x $(LAYER_DIR)/ocel/bootstrap
	cp -R platform/aws/functions/entrypoints/dist/. $(LAYER_DIR)/ocel/
	rm -f $(LAYER_ZIP)
	cd $(LAYER_DIR) && zip -r $(CURDIR)/$(LAYER_ZIP) ocel

listener:
	rm -rf $(LISTENER_DIR)
	mkdir -p $(LISTENER_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -tags lambda.norpc -ldflags="-s -w" \
	  -o $(CURDIR)/$(LISTENER_DIR)/bootstrap ./platform/aws/provider/cmd/listener
	chmod +x $(LISTENER_DIR)/bootstrap
	rm -f $(LISTENER_ZIP)
	cd $(LISTENER_DIR) && zip -j $(CURDIR)/$(LISTENER_ZIP) bootstrap

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
