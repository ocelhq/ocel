package payloads

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"

	"github.com/ocelhq/ocel/pkg/providerkit/arch"
)

//go:generate pnpm --dir ../../../.. exec turbo run generate --filter=@platform/aws-payloads

//go:embed dist
var embedded embed.FS

type Payload struct {
	Bytes          []byte
	SHA256         string
	ChecksumSHA256 string
}

var (
	runtimeLayers = map[string]Payload{
		"amd64": load("runtime-layer-amd64.zip"),
		"arm64": load("runtime-layer-arm64.zip"),
	}
	containerRuntimes = map[string]Payload{
		"amd64": load("container-runtime-amd64"),
		"arm64": load("container-runtime-arm64"),
	}
	uploadCompleter = load("upload-completer.zip")
	imageOptimizer  = load("image-optimizer.zip")
	revalidator     = load("revalidator.zip")
	tagPublisher    = load("tag-publisher.zip")
	tagInvalidator  = load("tag-invalidator.zip")
)

func RuntimeLayer(architecture string) (Payload, error) {
	goarch, builds := arch.GoArch(architecture)
	if !builds {
		return Payload{}, fmt.Errorf("this provider carries no runtime built for %q", architecture)
	}
	return runtimeLayers[goarch], nil
}

func ContainerRuntime(arch string) (Payload, error) {
	held, builds := containerRuntimes[arch]
	if !builds {
		return Payload{}, fmt.Errorf("this provider carries no container runtime built for %q", arch)
	}
	return held, nil
}

func UploadCompleter() Payload { return uploadCompleter }

func ImageOptimizer() Payload { return imageOptimizer }

func Revalidator() Payload { return revalidator }

func TagPublisher() Payload { return tagPublisher }

func TagInvalidator() Payload { return tagInvalidator }

func Of(data []byte) Payload {
	sum := sha256.Sum256(data)
	return Payload{
		Bytes:          data,
		SHA256:         hex.EncodeToString(sum[:]),
		ChecksumSHA256: base64.StdEncoding.EncodeToString(sum[:]),
	}
}

func load(name string) Payload {
	data, err := embedded.ReadFile(path.Join("dist", name))
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	return Of(data)
}
