package host

import (
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
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
	SwitchboardAddress   = SwitchboardContainer + ":" + switchboardPort
)

var switchboardCapabilities = []string{"DAC_OVERRIDE", "DAC_READ_SEARCH"}

func switchboardBinary(arch string) []byte { return embedded(switchboard.Name, arch) }

func switchboardStanding(binary []byte) boxContainer { return switchboardOver(contentSum(binary)) }

func switchboardOver(sum string) boxContainer {
	return boxContainer{
		name:  SwitchboardContainer,
		image: SwitchboardImage,
		command: []string{SwitchboardMounted, "serve",
			"--listen", ":" + switchboardPort,
			"--table", live.RoutingTable,
			"--trust", caddy.Container,
		},
		config: sum,
		binds: []string{
			SwitchboardDir + ":" + switchboardMount + ":ro",
			live.RoutingDir + ":" + live.RoutingDir + ":ro",
			ConnectorRun + ":" + ConnectorRun + ":ro",
			switchboard.ControlDir + ":" + switchboard.ControlDir,
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

func factOf(facts []byte, name string) string {
	for line := range strings.Lines(string(facts)) {
		if value, held := strings.CutPrefix(strings.TrimSpace(line), name+"="); held {
			return value
		}
	}
	return ""
}
