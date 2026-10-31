package albresources

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/transform"
)

const (
	ExternalManaged  = "EXTERNAL_MANAGED"
	PremiumTier      = "PREMIUM"
	HTTPSPortRange   = "443"
	ServerlessNEG    = "SERVERLESS"
	ObjectViewerRole = "roles/storage.objectViewer"
)

const (
	OutputAddress        = "address"
	OutputCertificateMap = "certificateMap"
	OutputURLMap         = "urlMap"
	OutputNotFound       = "notFound"
	OutputAssetsBackend  = "assetsBackend"
	OutputGateVersion    = "gateVersion"
)

type LoadBalancerSpec struct {
	Namespace string
	Region    string
	Names     Names
	Tier      environment.Tier
	Assets    *AssetsBucket
	Preview   PreviewEntry
	Patches   transform.Result
}

type AssetsBucket struct {
	Name  string
	Agent string
}

type PreviewEntry struct {
	BaseDomain  string `json:"baseDomain"`
	Certificate string `json:"certificate,omitempty"`
}

type BindingSpec struct {
	Namespace      string
	Region         string
	Slug           string
	Tier           environment.Tier
	CertificateMap string
	Hosts          map[string]BindingHost
	Patches        map[string]transform.Result
}

type BindingHost struct {
	App         string
	Certificate string
	Service     string
	Tag         string
	Backend     string
}

type GateSpec struct {
	Names  Names
	Main   GateVersion
	Before []GateVersion
}

type GateVersion struct {
	ID     string `json:"id"`
	Image  string `json:"image"`
	Config string `json:"config"`
}
