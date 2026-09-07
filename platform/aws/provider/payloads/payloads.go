package payloads

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
	membraneLayers = map[string]Payload{
		"amd64": load("membrane-layer-amd64.zip"),
		"arm64": load("membrane-layer-arm64.zip"),
	}
	uploadCompleter = load("upload-completer.zip")
	imageOptimizer  = load("image-optimizer.zip")
	revalidator     = load("revalidator.zip")
	tagPublisher    = load("tag-publisher.zip")
	tagInvalidator  = load("tag-invalidator.zip")
)

func MembraneLayer(arch string) (Payload, error) {
	goarch, builds := providerkit.GoArch(arch)
	if !builds {
		return Payload{}, fmt.Errorf("this provider carries no membrane built for %q", arch)
	}
	return membraneLayers[goarch], nil
}

func UploadCompleter() Payload { return uploadCompleter }

func ImageOptimizer() Payload { return imageOptimizer }

func Revalidator() Payload { return revalidator }

func TagPublisher() Payload { return tagPublisher }

func TagInvalidator() Payload { return tagInvalidator }

func load(name string) Payload {
	data, err := embedded.ReadFile(path.Join("dist", name))
	if err != nil {
		panic(fmt.Sprintf("payloads: %v", err))
	}
	sum := sha256.Sum256(data)
	return Payload{
		Bytes:          data,
		SHA256:         hex.EncodeToString(sum[:]),
		ChecksumSHA256: base64.StdEncoding.EncodeToString(sum[:]),
	}
}
