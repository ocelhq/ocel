package traefik

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	FileName = "ocel.yml"
	priority = 1000000
)

const (
	service    = "ocel-switchboard"
	redirect   = "ocel-https"
	noop       = "noop@internal"
	httpSuffix = "-http"
)

const (
	reloadInterval = time.Second
	reloadPauses   = 30
)

type Box interface {
	Ran(ctx context.Context, what string, argv []string) (string, error)
	ReadBeside(ctx context.Context, path string) ([]switchboard.SiblingFile, error)
	ReadSpec(ctx context.Context) (proxy.Spec, error)
	ProbeAnyCertificate(ctx context.Context, hostname string) (answered, failure string, err error)
	Pause(ctx context.Context, wait time.Duration) error
	PlacedSum(ctx context.Context, path string) (string, error)
	ReadLeaf(ctx context.Context, hostname string) ([]byte, error)
}

type Traefik struct {
	Box                Box
	Directory          string
	ContainerDirectory string
	Resolver           string
	PreviewResolver    string
	HTTP               string
	HTTPS              string
	Network            string
	Port               int
}

func routerName(hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	return "ocel-" + strings.ReplaceAll(hostname, ".", "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

func (t Traefik) directory() string { return filepath.Clean(t.Directory) }

func (t Traefik) containerDirectory() string {
	if t.ContainerDirectory == "" {
		return t.directory()
	}
	return filepath.Clean(t.ContainerDirectory)
}

func (t Traefik) file() string { return filepath.Join(t.directory(), FileName) }

func (t Traefik) upstream() string {
	if t.Network != "" {
		return "http://" + switchboard.Name + ":" + switchboard.HTTPSListenPort
	}
	return "http://127.0.0.1:" + strconv.Itoa(t.Port)
}
