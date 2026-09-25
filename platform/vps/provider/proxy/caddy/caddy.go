package caddy

import (
	"context"
	"strings"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const Image = "public.ecr.aws/docker/library/caddy@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"

const (
	Container   = "ocel-proxy"
	AdminSocket = "/run/caddy-admin.sock"
	AdminPort   = 2019
	ConfigName  = "caddy.json"
	ConfigDir   = "/etc/caddy/ocel"
	ConfigMount = ConfigDir + "/" + ConfigName
	DataMount   = "/data"
	PinsMount   = "/etc/caddy/pins"
	PinsDir     = live.ClassRoot + "/certs"
	HTTPPort    = "80"
	HTTPSPort   = "443"
)

const Grace = 30 * time.Second

const (
	pinCertificate = ".crt"
	pinKey         = ".key"
)

func PinCertificate(path string) string { return path + pinCertificate }

func PinKey(path string) string { return path + pinKey }

func Command() []string { return []string{"caddy", "run", "--config", ConfigMount} }

type Box interface {
	Ran(ctx context.Context, what, command string) (string, error)
	Said(ctx context.Context, command string) (string, error)
	Loopback(ctx context.Context, hostname string) ([]byte, error)
}

type Builtin struct{ box Box }

func New(box Box) *Builtin { return &Builtin{box: box} }

func quoted(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func words(argv []string) string {
	quotedArgs := make([]string, 0, len(argv))
	for _, arg := range argv {
		quotedArgs = append(quotedArgs, quoted(arg))
	}
	return strings.Join(quotedArgs, " ")
}

func inside(argv ...string) string {
	return words(append([]string{"docker", "exec", Container}, argv...))
}

func (b *Builtin) Trouble(ctx context.Context, hostname string) (error, error) {
	logged, err := b.box.Said(ctx, logging())
	if err != nil {
		return nil, err
	}
	if limit, said := certs.RateLimited(logged); said && limit.Covers(hostname) && !limit.Spent(time.Now()) {
		return limit.Refusal(hostname), nil
	}
	return nil, nil
}
