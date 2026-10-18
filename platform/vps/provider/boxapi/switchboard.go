package boxapi

import "github.com/ocelhq/ocel/pkg/router"

const SwitchboardRouter router.Kind = "switchboard"

const (
	Switchboard              = "ocel-switchboard"
	SwitchboardControlDir    = "/run/" + Switchboard
	SwitchboardControlSocket = SwitchboardControlDir + "/control.sock"
	FrontDir                 = "/run/ocel-front"
	FrontSocket              = FrontDir + "/switchboard.sock"
	AdmitSocket              = FrontDir + "/admit.sock"
	AdmitPath                = "/admit"
	AdmitField               = "domain"
)

const (
	HTTPSListenPort  = "8443"
	TunnelListenPort = "8444"
)

const HeardHeader = "X-Ocel-Heard"

const (
	DrainExpired = "drain-expired"
	Drained      = "drained"
	Ungated      = "ungated"
	Answered     = "answered"
)

const (
	PlaceEnv     = "OCEL_SWITCHBOARD_PLACE"
	OriginPrefix = "ocel-origin-"
)

type SiblingFile struct {
	Name    string `json:"name"`
	Content []byte `json:"content"`
}
