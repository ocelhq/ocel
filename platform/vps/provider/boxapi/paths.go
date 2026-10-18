package boxapi

const (
	ProxyDir     = StateRoot + "/proxy"
	ProxyConfig  = ProxyDir + "/caddy.json"
	RoutingDir   = StateRoot + "/routing"
	RoutingTable = RoutingDir + "/table.json"
)

const (
	TierRoot  = "/etc/ocel"
	StateRoot = "/var/lib/ocel"
	LibDir    = "/usr/local/lib/ocel"
)

const (
	DeployLogin = "ocel-deploy"
	DockerShims = LibDir + "/bin"
	LoginShell  = LibDir + "/login"
	DeployPath  = DockerShims + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)
