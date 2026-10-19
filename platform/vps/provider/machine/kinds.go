package machine

const (
	KindNetwork      = "docker:network"
	KindContainer    = "docker:container"
	KindVolume       = "docker:volume"
	KindProxyConfig  = "ocel:proxy-config"
	KindRoutingTable = "ocel:routing-table"
	KindPlaced       = "ocel:placed-file"
)

const (
	KindApps        = "docker:app-containers"
	KindAppNetworks = "docker:app-networks"

	KindResourceVolumes = "docker:resource-volumes"
)

const (
	KindLiveRestore = "docker:live-restore"

	DaemonConfig    = "/etc/docker/daemon.json"
	LiveRestoreFact = "live-restore=decided\n"
	SwarmStates     = "active|pending|locked"
)

const (
	MoveGroupKind    = "proxy"
	KindCertificates = "tls:certificates"
)
