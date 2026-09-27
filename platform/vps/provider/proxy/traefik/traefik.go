package traefik

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	FileName = "ocel.yml"
	Priority = 1000000
)

const (
	service    = "ocel-switchboard"
	redirect   = "ocel-https"
	noop       = "noop@internal"
	httpSuffix = "-http"
)

type Box interface {
	Ran(ctx context.Context, what string, argv []string) (string, error)
	Beside(ctx context.Context, path string) ([]switchboard.Neighbour, error)
}

type Traefik struct {
	Box             Box
	Directory       string
	Resolver        string
	PreviewResolver string
	HTTP            string
	HTTPS           string
	Network         string
	Port            int
}

func RouterName(hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	return "ocel-" + strings.ReplaceAll(hostname, ".", "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

func (t Traefik) file() string { return filepath.Join(filepath.Clean(t.Directory), FileName) }

func (t Traefik) upstream() string {
	if t.Network != "" {
		return "http://" + switchboard.Name + ":" + switchboard.HTTPSListenPort
	}
	return "http://127.0.0.1:" + strconv.Itoa(t.Port)
}
