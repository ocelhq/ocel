package host

import (
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const SwitchboardImage = "gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2"

const (
	SwitchboardContainer = "ocel-switchboard"
	switchboardName      = "ocel-switchboard"
	SwitchboardDir       = helperRoot + "/switchboard"
	SwitchboardBinary    = SwitchboardDir + "/" + switchboardName
	switchboardMount     = "/ocel/switchboard"
	SwitchboardMounted   = switchboardMount + "/" + switchboardName
	SwitchboardControl   = "/run/ocel-switchboard"
	switchboardPort      = "8080"
	SwitchboardAddress   = SwitchboardContainer + ":" + switchboardPort
)

var switchboardCapabilities = []string{"DAC_OVERRIDE", "DAC_READ_SEARCH"}

func switchboardBinary(arch string) []byte { return embedded(switchboardName, arch) }

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
			SwitchboardControl + ":" + SwitchboardControl,
		},
		caps:      switchboardCapabilities,
		files:     []string{live.RoutingTable, SwitchboardBinary},
		answering: quoted(SwitchboardMounted) + " upstreams",
		answer:    "did not answer over its control socket",
		joins:     true,
	}
}

func switchboardRun() []string { return switchboardStanding(nil).run() }

func switchboardWriting(attempts int) string { return switchboardStanding(nil).writing(attempts) }

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
