package caddy

import (
	"context"
	"strings"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

const Image = "public.ecr.aws/docker/library/caddy@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"

const (
	Container   = proxy.BuiltinContainer
	AdminSocket = "/run/caddy-admin.sock"
	AdminPort   = 2019
	ConfigName  = "caddy.json"
	ConfigDir   = "/etc/caddy/ocel"
	ConfigMount = ConfigDir + "/" + ConfigName
	DataMount   = "/data"
	PinsMount   = "/etc/caddy/pins"
	PinsDir     = live.ClassRoot + "/certs"
	HTTPPort    = proxy.HTTPPort
	HTTPSPort   = proxy.HTTPSPort
)

const Grace = 30 * time.Second

const (
	pinCertificate = ".crt"
	pinKey         = ".key"
)

func PinCertificate(path string) string { return path + pinCertificate }

func PinKey(path string) string { return path + pinKey }

func Pinned(path string) (string, bool) {
	leaf, beneath := strings.CutPrefix(path, PinsDir+"/")
	return leaf, beneath && leaf != "" && !strings.Contains(leaf, "/") && leaf != "." && leaf != ".."
}

func Command() []string { return []string{"caddy", "run", "--config", ConfigMount} }

const loadedApp = "/config/apps/http"

func Ready(reader string) []string { return []string{reader, "answers", AdminSocket, loadedApp} }

type Box interface {
	Ran(ctx context.Context, what string, argv []string) (string, error)
	Said(ctx context.Context, argv []string) (string, error)
}

type Builtin struct{ Box Box }

func inside(argv ...string) []string {
	return append([]string{"docker", "exec", Container}, argv...)
}
