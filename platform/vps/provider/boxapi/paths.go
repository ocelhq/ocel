package boxapi

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	ProxyDir         = StateRoot + "/proxy"
	ProxyConfig      = ProxyDir + "/caddy.json"
	RoutingDir       = StateRoot + "/routing"
	RoutingTablePath = RoutingDir + "/table.json"
)

const (
	TierRoot  = "/etc/ocel"
	StateRoot = "/var/lib/ocel"
	LibDir    = "/usr/local/lib/ocel"
)

const LiveBinary = LibDir + "/live"

const DeployUser = "ocel-deploy"

const (
	ReleasesDir    = StateRoot + "/releases"
	ReleasesHelper = LibDir + "/releases"
	ImagesLock     = ReleasesDir + "/images.lock"
)

func TierDir(tier environment.Tier) string { return TierRoot + "/" + string(tier) }

func StateDir(tier environment.Tier) string { return StateRoot + "/" + string(tier) }

func Sum(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

const (
	PinsDir        = TierRoot + "/certs"
	pinCertificate = ".crt"
	pinKey         = ".key"
)

func PinCertificate(path string) string { return path + pinCertificate }

func PinKey(path string) string { return path + pinKey }

func Pinned(path string) (string, bool) {
	leaf, beneath := strings.CutPrefix(path, PinsDir+"/")
	return leaf, beneath && leaf != "" && !strings.Contains(leaf, "/") && leaf != "." && leaf != ".."
}

const (
	DeployLogin = "ocel-deploy"
	DockerShims = LibDir + "/bin"
	LoginShell  = LibDir + "/login"
	DeployPath  = DockerShims + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)
