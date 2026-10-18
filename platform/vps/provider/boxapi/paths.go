package boxapi

import "strings"

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
