package host

import (
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const SwitchboardImage = "gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2"

const (
	SwitchboardContainer = switchboard.Name
	SwitchboardDir       = helperRoot + "/switchboard"
	SwitchboardBinary    = SwitchboardDir + "/" + switchboard.Name
	switchboardMount     = "/ocel/switchboard"
	SwitchboardMounted   = switchboardMount + "/" + switchboard.Name
	switchboardPort      = "8080"
	SwitchboardUpstream  = "unix/" + switchboard.FrontSocket
)

var switchboardCapabilities = []string{"DAC_OVERRIDE", "DAC_READ_SEARCH"}

func switchboardBinary(arch string) []byte { return embedded(switchboard.Name, arch) }

func switchboardStanding(binary []byte, front Front) boxContainer {
	var relaying []string
	if front.adopted() {
		relaying = []string{"--relay", switchboard.OwnNetwork}
	}
	return boxContainer{
		name:  SwitchboardContainer,
		image: SwitchboardImage,
		command: append([]string{SwitchboardMounted, "serve",
			"--listen", ":" + switchboardPort,
			"--front", switchboard.FrontSocket,
			"--table", live.RoutingTable,
		}, relaying...),
		ports:  front.published(),
		config: contentSum(binary),
		binds: []string{
			SwitchboardDir + ":" + switchboardMount + ":ro",
			live.RoutingDir + ":" + live.RoutingDir + ":ro",
			ConnectorRun + ":" + ConnectorRun + ":ro",
			switchboard.ControlDir + ":" + switchboard.ControlDir,
			switchboard.FrontDir + ":" + switchboard.FrontDir,
		},
		caps:    switchboardCapabilities,
		files:   []string{live.RoutingTable, SwitchboardBinary},
		ready:   []string{SwitchboardMounted, "upstreams"},
		unready: "answered nothing over its control socket in " + switchboard.ControlDir,
		inodes:  []string{SwitchboardMounted, "inodes"},
		joins:   true,
	}
}

func switchboardCommand(argv ...string) []string {
	return append([]string{"docker", "exec", SwitchboardContainer, SwitchboardMounted}, argv...)
}
