package payloads

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
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
	membraneLayer   = load("membrane-layer.zip")
	uploadCompleter = load("upload-completer.zip")
	imageOptimizer  = load("image-optimizer.zip")
	revalidator     = load("revalidator.zip")
	tagPublisher    = load("tag-publisher.zip")
	tagInvalidator  = load("tag-invalidator.zip")
)

func MembraneLayer() Payload { return membraneLayer }

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
