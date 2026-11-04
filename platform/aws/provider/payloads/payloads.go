package payloads

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/images"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/aws-payloads

type Payload struct {
	Bytes          []byte
	SHA256         string
	ChecksumSHA256 string
}

var (
	//go:embed dist/runtime-layer-amd64.zip
	runtimeLayerAMD64 []byte
	//go:embed dist/runtime-layer-amd64.zip.sha256
	runtimeLayerAMD64SHA256 string

	//go:embed dist/runtime-layer-arm64.zip
	runtimeLayerARM64 []byte
	//go:embed dist/runtime-layer-arm64.zip.sha256
	runtimeLayerARM64SHA256 string

	//go:embed dist/container-runtime-amd64
	containerRuntimeAMD64 []byte
	//go:embed dist/container-runtime-amd64.sha256
	containerRuntimeAMD64SHA256 string

	//go:embed dist/container-runtime-arm64
	containerRuntimeARM64 []byte
	//go:embed dist/container-runtime-arm64.sha256
	containerRuntimeARM64SHA256 string

	//go:embed dist/upload-completer.zip
	uploadCompleter []byte
	//go:embed dist/upload-completer.zip.sha256
	uploadCompleterSHA256 string

	//go:embed dist/envsourcesync.zip
	envSourceSync []byte
	//go:embed dist/envsourcesync.zip.sha256
	envSourceSyncSHA256 string

	//go:embed dist/realtime-authorizer.zip
	realtimeAuthorizer []byte
	//go:embed dist/realtime-authorizer.zip.sha256
	realtimeAuthorizerSHA256 string

	//go:embed dist/image-optimizer.zip
	imageOptimizer []byte
	//go:embed dist/image-optimizer.zip.sha256
	imageOptimizerSHA256 string

	//go:embed dist/tag-invalidator.zip
	tagInvalidator []byte
	//go:embed dist/tag-invalidator.zip.sha256
	tagInvalidatorSHA256 string

	//go:embed dist/next-container/entrypoint.mjs
	nextContainerEntrypoint []byte
)

func NextContainerRuntime() map[string][]byte {
	return map[string][]byte{images.RuntimeEntrypointFile: nextContainerEntrypoint}
}

func RuntimeLayer(architecture string) (Payload, error) {
	goarch, builds := arch.GoArch(architecture)
	switch {
	case builds && goarch == "amd64":
		return shipped(runtimeLayerAMD64, runtimeLayerAMD64SHA256), nil
	case builds && goarch == "arm64":
		return shipped(runtimeLayerARM64, runtimeLayerARM64SHA256), nil
	}
	return Payload{}, refuseUnbuilt("runtime", architecture)
}

func RuntimeLayerSHA256(architecture string) (string, error) {
	goarch, builds := arch.GoArch(architecture)
	switch {
	case builds && goarch == "amd64":
		return digestOf(runtimeLayerAMD64SHA256), nil
	case builds && goarch == "arm64":
		return digestOf(runtimeLayerARM64SHA256), nil
	}
	return "", refuseUnbuilt("runtime", architecture)
}

func ContainerRuntime(architecture string) (Payload, error) {
	switch architecture {
	case "amd64":
		return shipped(containerRuntimeAMD64, containerRuntimeAMD64SHA256), nil
	case "arm64":
		return shipped(containerRuntimeARM64, containerRuntimeARM64SHA256), nil
	}
	return Payload{}, refuseUnbuilt("container runtime", architecture)
}

func UploadCompleter() Payload { return shipped(uploadCompleter, uploadCompleterSHA256) }

func EnvSourceSync() Payload { return shipped(envSourceSync, envSourceSyncSHA256) }

func EnvSourceSyncSHA256() string { return digestOf(envSourceSyncSHA256) }

func RealtimeAuthorizer() Payload { return shipped(realtimeAuthorizer, realtimeAuthorizerSHA256) }

func ImageOptimizer() Payload { return shipped(imageOptimizer, imageOptimizerSHA256) }

func ImageOptimizerSHA256() string { return digestOf(imageOptimizerSHA256) }

func TagInvalidator() Payload { return shipped(tagInvalidator, tagInvalidatorSHA256) }

func TagInvalidatorSHA256() string { return digestOf(tagInvalidatorSHA256) }

func refuseUnbuilt(what, architecture string) error {
	return fmt.Errorf("this provider ships no %s built for %q", what, architecture)
}

func digestOf(generated string) string { return strings.TrimSpace(generated) }

func shipped(data []byte, generated string) Payload {
	digest := digestOf(generated)
	sum, err := hex.DecodeString(digest)
	if err != nil {
		panic(fmt.Sprintf("payloads: a generated digest %q is not hex: %v", digest, err))
	}
	return Payload{Bytes: data, SHA256: digest, ChecksumSHA256: base64.StdEncoding.EncodeToString(sum)}
}

func Of(data []byte) Payload {
	sum := sha256.Sum256(data)
	return Payload{
		Bytes:          data,
		SHA256:         hex.EncodeToString(sum[:]),
		ChecksumSHA256: base64.StdEncoding.EncodeToString(sum[:]),
	}
}
